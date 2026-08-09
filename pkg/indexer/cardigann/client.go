package cardigann

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/stats"
)

// Client adapts a tracker definition to SeedStream's Indexer interface, so a
// scraped tracker is indistinguishable from a Torznab one everywhere else in the
// application — search, ranking, playback and the watchdog all work unchanged.
type Client struct {
	engine *Engine
	name   string

	// Counters behind the stats page. A scraped tracker has no published quota,
	// but every other number the page shows is measured locally and applies
	// just as well here as to a Torznab indexer — reporting zeros made a
	// working tracker look idle.
	mu              sync.RWMutex
	apiUsed         int
	searchesCount   int
	totalResponseMS int64

	// usageManager persists the counters across restarts and holds the
	// all-time totals. nil disables persistence; the live counters still work.
	usageManager *indexer.UsageManager
	// apiLimit and downloadLimit are the operator's configured daily ceilings,
	// 0 for unlimited. Scraping has no tracker-imposed quota, but an operator
	// may still want to bound how hard SeedStream hits a site.
	apiLimit      int
	downloadLimit int
}

// NewClient builds an indexer client for a definition id from the catalog.
//
// um persists the counters across restarts and holds the all-time totals; nil
// keeps them in memory only, which is what tests want and what a caller with no
// state manager gets.
func NewClient(cat *Catalog, definitionID, displayName, baseURLOverride string, settings map[string]string, timeout time.Duration, um *indexer.UsageManager, indexerConfigs ...config.IndexerConfig) (*Client, error) {
	def, ok := cat.Get(definitionID)
	if !ok {
		return nil, fmt.Errorf("tracker definition %q is not installed", definitionID)
	}
	eng, err := NewEngine(def, baseURLOverride, settings, timeout, indexerConfigs...)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = def.Name
	}
	c := &Client{engine: eng, name: name, usageManager: um}
	for _, cfg := range indexerConfigs {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), name) {
			c.apiLimit = cfg.APIHitsDay
			c.downloadLimit = cfg.DownloadsDay
			break
		}
	}
	// Restore today's counters so a restart does not reset the page to zero.
	if um != nil {
		if usage := um.GetIndexerUsage(name); usage != nil {
			c.apiUsed = usage.APIHitsUsed
		}
	}
	return c, nil
}

// Name identifies this tracker in logs, stats and the stream list.
func (c *Client) Name() string { return c.name }

// BaseURL is the tracker address in use, after any operator override.
func (c *Client) BaseURL() string { return c.engine.BaseURL() }

// DefinitionID is the definition this tracker was built from.
func (c *Client) DefinitionID() string { return c.engine.Definition().ID }

// Ping verifies the credentials by performing a login.
func (c *Client) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.engine.Login(ctx)
}

// GetUsage reports what this tracker has actually done.
//
// A scraped tracker publishes no quota, which is why the limits are whatever
// the operator configured and nothing is invented. Everything else — requests
// made, searches run, average response time, all-time totals — is measured the
// same way as for a Torznab indexer, and was previously reported as zeros that
// made a working tracker indistinguishable from an idle one.
func (c *Client) GetUsage() indexer.Usage {
	// Read the shared counters before taking the local lock, never while
	// holding it: the usage manager takes its own lock, and reversing the two
	// orders between here and recordSearch would be a deadlock waiting on load.
	var allTimeHits, allTimeDownloads, downloadsUsed int
	if c.usageManager != nil {
		if usage := c.usageManager.GetIndexerUsage(c.name); usage != nil {
			allTimeHits = usage.AllTimeAPIHitsUsed
			allTimeDownloads = usage.AllTimeDownloadsUsed
			// Grabs are counted at playback, against the usage manager rather
			// than this client, so today's total has to be read back from there
			// or the download column stays at zero however much was grabbed.
			downloadsUsed = usage.DownloadsUsed
		}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	u := indexer.Usage{
		APIHitsLimit:         c.apiLimit,
		APIHitsUsed:          c.apiUsed,
		SearchesCount:        c.searchesCount,
		DownloadsLimit:       c.downloadLimit,
		DownloadsUsed:        downloadsUsed,
		AllTimeAPIHitsUsed:   allTimeHits,
		AllTimeDownloadsUsed: allTimeDownloads,
	}
	// A remaining figure only means something against a limit. Reporting one
	// where the operator set none would invent a budget the tracker never gave.
	if c.apiLimit > 0 {
		if remaining := c.apiLimit - c.apiUsed; remaining > 0 {
			u.APIHitsRemaining = remaining
		}
	}
	if c.downloadLimit > 0 {
		if remaining := c.downloadLimit - downloadsUsed; remaining > 0 {
			u.DownloadsRemaining = remaining
		}
	}
	if c.searchesCount > 0 {
		u.AvgResponseMS = float64(c.totalResponseMS) / float64(c.searchesCount)
	}
	return u
}

// recordSearch updates the local counters and the persisted event log for one
// completed search. Failures count as requests too: a search that errored still
// hit the tracker, and hiding those would make a broken tracker look quiet
// rather than broken.
func (c *Client) recordSearch(success bool, elapsedMS int64, resultCount int) {
	c.mu.Lock()
	c.apiUsed++
	c.searchesCount++
	c.totalResponseMS += elapsedMS
	c.mu.Unlock()

	if c.usageManager != nil {
		c.usageManager.IncrementUsed(c.name, 1, 0)
	}
	stats.Default().RecordIndexerSearch(c.name, success, elapsedMS, resultCount)
}

// DownloadNZB exists to satisfy the interface. These are torrent trackers, so
// there is never an NZB to fetch.
func (c *Client) DownloadNZB(ctx context.Context, url string) ([]byte, error) {
	return nil, fmt.Errorf("%s is a torrent tracker, not a Usenet indexer", c.name)
}

// Search runs the query against the tracker and returns results in the same
// shape a Torznab indexer would, recording the same statistics event the
// Torznab client records so both kinds of tracker appear in search history.
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	startedAt := time.Now()
	resp, err := c.search(req)
	resultCount := 0
	if resp != nil {
		resultCount = len(resp.Channel.Items)
	}
	c.recordSearch(err == nil, time.Since(startedAt).Milliseconds(), resultCount)
	return resp, err
}

func (c *Client) search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	query := map[string]string{}
	if req.Season != "" {
		query["season"] = req.Season
	}
	if req.Episode != "" {
		query["ep"] = req.Episode
	}
	if req.IMDbID != "" {
		query["imdbid"] = req.IMDbID
	}
	if req.TVDBID != "" {
		query["tvdbid"] = req.TVDBID
	}
	if req.TMDBID != "" {
		query["tmdbid"] = req.TMDBID
	}

	cats := c.categoriesFor(req.Cat)
	results, err := c.engine.Search(ctx, req.Query, cats, query)
	if err != nil {
		return nil, fmt.Errorf("%s search: %w", c.name, err)
	}

	resp := &indexer.SearchResponse{}
	for _, r := range results {
		resp.Channel.Items = append(resp.Channel.Items, c.toItem(r))
	}
	logger.Debug("cardigann: search complete", "tracker", c.name, "results", len(resp.Channel.Items))
	if req.Limit > 0 && len(resp.Channel.Items) > req.Limit {
		resp.Channel.Items = resp.Channel.Items[:req.Limit]
	}
	return resp, nil
}

// categoriesFor maps the Newznab categories SeedStream searches by onto the
// tracker's own category ids, using the definition's mapping table.
func (c *Client) categoriesFor(requested string) []string {
	want := map[string]bool{}
	for _, part := range strings.Split(requested, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		switch {
		case n >= 2000 && n < 3000:
			want["movies"] = true
		case n >= 5000 && n < 6000:
			want["tv"] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	var out []string
	for _, m := range c.engine.Definition().Caps.CategoryMappings {
		cat := strings.ToLower(m.Cat)
		if want["movies"] && strings.HasPrefix(cat, "movies") {
			out = append(out, m.ID)
		}
		if want["tv"] && strings.HasPrefix(cat, "tv") {
			out = append(out, m.ID)
		}
	}
	return out
}

// toItem converts one scraped release into the item shape the rest of
// SeedStream consumes, including the Torznab attributes it reads for seeders,
// info hash and magnet.
func (c *Client) toItem(r Result) indexer.Item {
	link := r.Magnet
	if link == "" {
		link = r.Download
	}
	item := indexer.Item{
		Title:    r.Title,
		Link:     link,
		GUID:     r.Details,
		PubDate:  r.PublishDate,
		Size:     r.Size,
		Comments: r.Details,
		Category: r.Category,
	}
	add := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			item.Attributes = append(item.Attributes, indexer.Attribute{Name: name, Value: value})
		}
	}
	// seeders is always published, including a genuine zero, so the swarm-health
	// checks can tell "no seeders" apart from "tracker did not say".
	item.Attributes = append(item.Attributes, indexer.Attribute{
		Name: "seeders", Value: strconv.Itoa(r.Seeders),
	})
	add("peers", strconv.Itoa(r.Seeders+r.Leechers))
	add("leechers", strconv.Itoa(r.Leechers))
	if r.Grabs > 0 {
		add("grabs", strconv.Itoa(r.Grabs))
	}
	add("magneturl", r.Magnet)
	add("infohash", r.InfoHash)
	if r.Download != "" {
		item.Enclosure = indexer.Enclosure{URL: r.Download, Length: r.Size, Type: "application/x-bittorrent"}
	}
	return item
}

// Ensure a Cardigann tracker is usable anywhere an Indexer is expected.
var _ indexer.Indexer = (*Client)(nil)
