package indexer

import (
	"context"
	"encoding/xml"
	"strconv"
	"strings"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
)

type Indexer interface {
	Search(req SearchRequest) (*SearchResponse, error)
	DownloadNZB(ctx context.Context, nzbURL string) ([]byte, error)
	Ping() error
	Name() string
	GetUsage() Usage
}

type Usage struct {
	APIHitsLimit         int
	APIHitsUsed          int
	APIHitsRemaining     int
	DownloadsLimit       int
	DownloadsUsed        int
	DownloadsRemaining   int
	AllTimeAPIHitsUsed   int
	AllTimeDownloadsUsed int
	SearchesCount        int
	AvgResponseMS        float64
}

type SearchRequest struct {
	Query                   string
	IMDbID                  string
	TMDBID                  string
	TVDBID                  string
	Cat                     string
	Limit                   int
	Season                  string
	Episode                 string
	SeriesSearchScope       string
	SearchMode              string
	DisableResultFiltering  bool
	EnableYearValidation    bool
	IndexerMode             string
	ValidationQuery         string
	ValidationQueries       []string
	ValidationQueryProfiles []ValidationQueryProfile
	StreamLabel             string `json:"-"`
	RequestLabel            string `json:"-"`

	EffectiveByIndexer map[string]*config.IndexerSearchConfig `json:"-"`

	OptionalOverrides *config.IndexerSearchConfig `json:"-"`
}

type ValidationQueryProfile struct {
	Languages []string
	Query     string
}

type SearchResponse struct {
	XMLName  xml.Name           `xml:"rss"`
	Channel  Channel            `xml:"channel"`
	Releases []*release.Release `xml:"-"`
}

type NewznabResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

type Channel struct {
	Response NewznabResponse `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
	Items    []Item          `xml:"item"`
}

type Item struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	GUID        string      `xml:"guid"`
	PubDate     string      `xml:"pubDate"`
	Category    string      `xml:"category"`
	Description string      `xml:"description"`
	Comments    string      `xml:"comments"`
	Size        int64       `xml:"size"`
	Enclosure   Enclosure   `xml:"enclosure"`
	Attributes  []Attribute `xml:"attr"`

	SourceIndexer Indexer `xml:"-"`

	ActualIndexer string `xml:"-"`

	ActualGUID string `xml:"-"`

	QuerySource string `xml:"-"`

	Duration float64 `xml:"-"`

	// Uniqueness plumbing (NZBHydra2-style). Stamped by the aggregator after
	// dedup: IndexersInSearch = indexers that participated in the search;
	// IndexersWithResult = distinct indexers that returned this normalized title.
	IndexersInSearch   int `xml:"-"`
	IndexersWithResult int `xml:"-"`
}

type Attribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type Enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

func (i *Item) GetAttribute(name string) string {
	for _, attr := range i.Attributes {
		if attr.Name == name {
			return attr.Value
		}
	}
	return ""
}

// IsTorrent reports whether this item came from a Torznab tracker. The check
// mirrors the protocol detection in ToRelease so the two are always in sync.
func (i *Item) IsTorrent() bool {
	return i.GetAttribute("magneturl") != "" ||
		i.GetAttribute("infohash") != "" ||
		i.GetAttribute("seeders") != "" ||
		strings.Contains(strings.ToLower(i.Enclosure.Type), "bittorrent") ||
		strings.HasPrefix(strings.ToLower(i.Link), "magnet:")
}

func (i *Item) ToRelease() *release.Release {
	if i == nil {
		return nil
	}
	grabs := 0
	if s := i.GetAttribute("grabs"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			grabs = n
		}
	}

	var languages []string
	if lang := i.GetAttribute("language"); lang != "" {
		for _, part := range strings.Split(lang, ",") {
			if t := strings.TrimSpace(part); t != "" {
				languages = append(languages, t)
			}
		}
	}
	indexerName := i.ActualIndexer
	if indexerName == "" && i.SourceIndexer != nil {
		indexerName = i.SourceIndexer.Name()
	}

	// Torznab torrent attributes.
	magnet := i.GetAttribute("magneturl")
	infoHash := strings.ToLower(strings.TrimSpace(i.GetAttribute("infohash")))
	seeders := atoiOrZero(i.GetAttribute("seeders"))
	peers := atoiOrZero(i.GetAttribute("peers"))
	if peers == 0 {
		peers = atoiOrZero(i.GetAttribute("leechers"))
	}

	// A result is a torrent if it advertises any torrent-specific signal: a
	// magnet, an info hash, seeders, or a bittorrent enclosure type.
	protocol := "other"
	if magnet != "" || infoHash != "" || i.GetAttribute("seeders") != "" ||
		strings.Contains(strings.ToLower(i.Enclosure.Type), "bittorrent") ||
		strings.HasPrefix(strings.ToLower(i.Link), "magnet:") {
		protocol = "torrent"
	}
	if magnet == "" && strings.HasPrefix(strings.ToLower(i.Link), "magnet:") {
		magnet = i.Link
	}

	return &release.Release{
		Title:              i.Title,
		Link:               i.Link,
		DetailsURL:         i.ReleaseDetailsURL(),
		Size:               i.Size,
		Indexer:            indexerName,
		SourceIndexer:      i.SourceIndexer,
		PubDate:            i.PubDate,
		GUID:               i.GUID,
		QuerySource:        i.QuerySource,
		Grabs:              grabs,
		Languages:          languages,
		Duration:           i.Duration,
		Protocol:           protocol,
		Magnet:             magnet,
		InfoHash:           infoHash,
		Seeders:            seeders,
		Peers:              peers,
		IndexersInSearch:   i.IndexersInSearch,
		IndexersWithResult: i.IndexersWithResult,
	}
}

func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return 0
}

func (i *Item) ReleaseDetailsURL() string {
	if i.ActualGUID != "" && strings.Contains(i.ActualGUID, "://") {
		return i.ActualGUID
	}
	if i.Comments != "" && strings.Contains(i.Comments, "://") {
		return i.Comments
	}
	if i.GUID != "" && strings.Contains(i.GUID, "://") {
		return i.GUID
	}
	return ""
}

func NormalizeItem(item *Item) {
	if item == nil {
		return
	}
	if item.Link == "" && item.Enclosure.URL != "" {
		item.Link = item.Enclosure.URL
	}
	if item.Size <= 0 {
		if item.Enclosure.Length > 0 {
			item.Size = item.Enclosure.Length
		} else if s := item.GetAttribute("size"); s != "" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				item.Size = n
			}
		}
	}
}

func NormalizeSearchResponse(resp *SearchResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Channel.Items {
		NormalizeItem(&resp.Channel.Items[i])
	}
	resp.Releases = make([]*release.Release, 0, len(resp.Channel.Items))
	for i := range resp.Channel.Items {
		if rel := resp.Channel.Items[i].ToRelease(); rel != nil {
			resp.Releases = append(resp.Releases, rel)
		}
	}
}
