package cardigann

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
)

// Client adapts a tracker definition to SeedStream's Indexer interface, so a
// scraped tracker is indistinguishable from a Torznab one everywhere else in the
// application — search, ranking, playback and the watchdog all work unchanged.
type Client struct {
	engine *Engine
	name   string
}

// NewClient builds an indexer client for a definition id from the catalog.
func NewClient(cat *Catalog, definitionID, displayName, baseURLOverride string, settings map[string]string, timeout time.Duration) (*Client, error) {
	def, ok := cat.Get(definitionID)
	if !ok {
		return nil, fmt.Errorf("tracker definition %q is not installed", definitionID)
	}
	eng, err := NewEngine(def, baseURLOverride, settings, timeout)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = def.Name
	}
	return &Client{engine: eng, name: name}, nil
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

// GetUsage reports API budget. Scraped trackers have no published quota, so this
// is reported as unlimited rather than inventing a number.
func (c *Client) GetUsage() indexer.Usage { return indexer.Usage{} }

// DownloadNZB exists to satisfy the interface. These are torrent trackers, so
// there is never an NZB to fetch.
func (c *Client) DownloadNZB(ctx context.Context, url string) ([]byte, error) {
	return nil, fmt.Errorf("%s is a torrent tracker, not a Usenet indexer", c.name)
}

// Search runs the query against the tracker and returns results in the same
// shape a Torznab indexer would.
func (c *Client) Search(req indexer.SearchRequest) (*indexer.SearchResponse, error) {
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
