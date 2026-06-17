// Package cerberus is SeedStream's torrent health watchdog service.
//
// Each SeedStream instance tracks which info_hashes correspond to which
// content (IMDB/TMDB IDs). When a torrent stalls the watchdog reports the
// failure to the local blocklist and searches for a replacement. The
// blocklist and registry are stored in the local SQLite database. A
// BaseURL field will be added in a future release to report to and query
// the central Cerberus server, enabling community-wide torrent health data.
package cerberus

import (
	"time"

	"seedstream/pkg/core/persistence"
)

// Client is the local Cerberus client. Currently operates on local SQLite
// only; central server support is planned.
type Client struct {
	store *persistence.StateManager
}

// ContentIDs identifies a piece of content for torrent health tracking.
type ContentIDs struct {
	ImdbID  string
	TmdbID  string
	TvdbID  string
	Season  int
	Episode int
}

// TorrentRecord is returned by GetContentByHash and used by the watchdog.
type TorrentRecord struct {
	InfoHash     string
	ImdbID       string
	TmdbID       string
	TvdbID       string
	Season       int
	Episode      int
	Magnet       string
	ReleaseTitle string
	IndexerName  string
	AddedAt      time.Time
}

// New returns a Cerberus Client backed by the given StateManager.
func New(store *persistence.StateManager) *Client {
	if store == nil {
		return nil
	}
	return &Client{store: store}
}

// RegisterTorrent records a new info_hash → content mapping. Called when a
// torrent is first handed to qBittorrent for playback.
func (c *Client) RegisterTorrent(infoHash string, ids ContentIDs, magnet, title, indexerName string) error {
	if c == nil {
		return nil
	}
	return c.store.RegisterTorrent(infoHash, ids.ImdbID, ids.TmdbID, ids.TvdbID, ids.Season, ids.Episode, magnet, title, indexerName)
}

// ReportFailure marks an info_hash as bad (stalled, missing files, etc.).
func (c *Client) ReportFailure(infoHash, reason string) error {
	if c == nil {
		return nil
	}
	return c.store.ReportTorrentFailure(infoHash, reason)
}

// IsBlocked returns true if this info_hash is in the local blocklist.
func (c *Client) IsBlocked(infoHash string) bool {
	if c == nil {
		return false
	}
	return c.store.IsInfoHashBlocked(infoHash)
}

// GetBlockedHashes returns all blocked hashes for the given content.
func (c *Client) GetBlockedHashes(ids ContentIDs) []string {
	if c == nil {
		return nil
	}
	return c.store.GetBlockedHashes(ids.ImdbID, ids.TmdbID, ids.TvdbID, ids.Season, ids.Episode)
}

// GetContentByHash looks up what content an info_hash was registered for.
// Returns nil if the hash has not been seen before.
func (c *Client) GetContentByHash(infoHash string) *TorrentRecord {
	if c == nil {
		return nil
	}
	e := c.store.GetTorrentByHash(infoHash)
	if e == nil {
		return nil
	}
	return &TorrentRecord{
		InfoHash:     e.InfoHash,
		ImdbID:       e.ImdbID,
		TmdbID:       e.TmdbID,
		TvdbID:       e.TvdbID,
		Season:       e.Season,
		Episode:      e.Episode,
		Magnet:       e.Magnet,
		ReleaseTitle: e.ReleaseTitle,
		IndexerName:  e.IndexerName,
		AddedAt:      e.AddedAt,
	}
}
