// Package cerberus is SeedStream's torrent health watchdog service.
//
// Each SeedStream instance tracks which info_hashes correspond to which
// content (IMDB/TMDB IDs). When a torrent stalls the watchdog reports the
// failure to the local blocklist and searches for a replacement. The
// blocklist and registry are stored in the local SQLite database.
//
// Cerberus operates fully locally by default. An optional Backend can be
// attached (see WithBackend / NewHTTPBackend) to report failures to and pull
// community blocklist data from a central Cerberus server. The local SQLite
// store always remains the primary source of truth; a Backend only augments
// it, and every Backend call is best-effort — a backend outage never blocks
// playback or the watchdog.
package cerberus

import (
	"context"
	"strings"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
)

// backendCallTimeout bounds how long a synchronous backend query may take so a
// slow or unreachable central server never stalls the watchdog.
const backendCallTimeout = 4 * time.Second

// Backend is an optional remote endpoint for community-wide torrent health.
// Implementations must be safe for concurrent use and should return promptly;
// the local SQLite store is always consulted regardless of backend outcome.
type Backend interface {
	// ReportFailure shares a failed info_hash with the central server.
	ReportFailure(ctx context.Context, infoHash, reason string, ids ContentIDs) error
	// ReportHealthy shares that an info_hash streamed successfully.
	ReportHealthy(ctx context.Context, infoHash string, ids ContentIDs) error
	// BlockedHashes returns community-reported bad hashes for the content.
	BlockedHashes(ctx context.Context, ids ContentIDs) ([]string, error)
	// Name identifies the backend for logging.
	Name() string
}

// Client is the Cerberus client. It always uses the local SQLite store and,
// when configured, augments it with a remote Backend.
type Client struct {
	store   *persistence.StateManager
	backend Backend
}

// Option configures a Client.
type Option func(*Client)

// WithBackend attaches a remote Backend for community torrent-health data.
// Passing a nil backend is a no-op (local-only mode).
func WithBackend(b Backend) Option {
	return func(c *Client) {
		if b != nil {
			c.backend = b
		}
	}
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

// New returns a Cerberus Client backed by the given StateManager. Optional
// Options (e.g. WithBackend) configure remote reporting.
func New(store *persistence.StateManager, opts ...Option) *Client {
	if store == nil {
		return nil
	}
	c := &Client{store: store}
	for _, opt := range opts {
		opt(c)
	}
	if c.backend != nil {
		logger.Info("Cerberus backend attached", "backend", c.backend.Name())
	}
	return c
}

// HasBackend reports whether a remote Backend is attached.
func (c *Client) HasBackend() bool {
	return c != nil && c.backend != nil
}

// RegisterTorrent records a new info_hash → content mapping. Called when a
// torrent is first handed to qBittorrent for playback.
func (c *Client) RegisterTorrent(infoHash string, ids ContentIDs, magnet, title, indexerName string) error {
	if c == nil {
		return nil
	}
	return c.store.RegisterTorrent(infoHash, ids.ImdbID, ids.TmdbID, ids.TvdbID, ids.Season, ids.Episode, magnet, title, indexerName)
}

// ReportFailure marks an info_hash as bad (stalled, missing files, etc.) in the
// local blocklist and, if a backend is attached, reports it upstream
// (best-effort, asynchronous).
func (c *Client) ReportFailure(infoHash, reason string) error {
	if c == nil {
		return nil
	}
	err := c.store.ReportTorrentFailure(infoHash, reason)
	if c.backend != nil {
		ids := c.contentIDsForHash(infoHash)
		go c.reportFailureToBackend(infoHash, reason, ids)
	}
	return err
}

// ReportHealthy records that an info_hash streamed successfully. Local state is
// not modified (success is the default); when a backend is attached the signal
// is forwarded upstream (best-effort, asynchronous). Safe no-op without backend.
func (c *Client) ReportHealthy(infoHash string, ids ContentIDs) {
	if c == nil || c.backend == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
		defer cancel()
		if err := c.backend.ReportHealthy(ctx, infoHash, ids); err != nil {
			logger.Debug("Cerberus backend ReportHealthy failed", "backend", c.backend.Name(), "hash", infoHash, "err", err)
		}
	}()
}

func (c *Client) reportFailureToBackend(infoHash, reason string, ids ContentIDs) {
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	if err := c.backend.ReportFailure(ctx, infoHash, reason, ids); err != nil {
		logger.Debug("Cerberus backend ReportFailure failed", "backend", c.backend.Name(), "hash", infoHash, "err", err)
	}
}

// contentIDsForHash looks up the content a hash was registered for, so backend
// reports carry the content identifiers. Returns a zero value if unknown.
func (c *Client) contentIDsForHash(infoHash string) ContentIDs {
	rec := c.GetContentByHash(infoHash)
	if rec == nil {
		return ContentIDs{}
	}
	return ContentIDs{
		ImdbID:  rec.ImdbID,
		TmdbID:  rec.TmdbID,
		TvdbID:  rec.TvdbID,
		Season:  rec.Season,
		Episode: rec.Episode,
	}
}

// IsBlocked returns true if this info_hash is in the local blocklist.
func (c *Client) IsBlocked(infoHash string) bool {
	if c == nil {
		return false
	}
	return c.store.IsInfoHashBlocked(infoHash)
}

// GetBlockedHashes returns all blocked hashes for the given content. Local
// blocklist entries are always included; when a backend is attached its
// community-reported hashes are merged in (best-effort, short timeout).
func (c *Client) GetBlockedHashes(ids ContentIDs) []string {
	if c == nil {
		return nil
	}
	local := c.store.GetBlockedHashes(ids.ImdbID, ids.TmdbID, ids.TvdbID, ids.Season, ids.Episode)
	if c.backend == nil {
		return local
	}

	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	remote, err := c.backend.BlockedHashes(ctx, ids)
	if err != nil {
		logger.Debug("Cerberus backend BlockedHashes failed", "backend", c.backend.Name(), "err", err)
		return local
	}
	return mergeHashes(local, remote)
}

// mergeHashes combines two hash lists, de-duplicating case-insensitively.
func mergeHashes(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, h := range list {
			key := normalizeHashKey(h)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, h)
		}
	}
	return out
}

// normalizeHashKey lowercases and trims a hash for use as a de-dupe map key.
func normalizeHashKey(h string) string {
	return strings.ToLower(strings.TrimSpace(h))
}

// PruneOldEntries removes registry entries older than maxAgeDays days from the
// local store to prevent unbounded database growth. Returns the count pruned.
func (c *Client) PruneOldEntries(maxAgeDays int) int64 {
	if c == nil || maxAgeDays <= 0 {
		return 0
	}
	maxAgeMs := int64(maxAgeDays) * 24 * 60 * 60 * 1000
	n, err := c.store.PruneOldRegistryEntries(maxAgeMs)
	if err != nil {
		logger.Warn("Cerberus: failed to prune old registry entries", "err", err)
		return 0
	}
	if n > 0 {
		logger.Info("Cerberus: pruned old registry entries", "count", n, "max_age_days", maxAgeDays)
	}
	return n
}

// KnownTorrentsForContent returns torrents already registered for this exact
// content, newest first, with locally blocked hashes removed.
//
// SeedStream registers every torrent it hands to qBittorrent, so this is the
// memory of what the seedbox has already downloaded. Replaying a title can use
// it to go straight back to that torrent instead of fetching a fresh one.
// Entries are matched on season, episode and at least one content ID, so a
// result is always the same title that was asked for.
func (c *Client) KnownTorrentsForContent(ids ContentIDs) []TorrentRecord {
	if c == nil {
		return nil
	}
	entries := c.store.GetRegisteredTorrentsForContent(ids.ImdbID, ids.TmdbID, ids.TvdbID, ids.Season, ids.Episode)
	if len(entries) == 0 {
		return nil
	}
	out := make([]TorrentRecord, 0, len(entries))
	for _, e := range entries {
		if c.store.IsInfoHashBlocked(e.InfoHash) {
			continue
		}
		out = append(out, TorrentRecord{
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
		})
	}
	return out
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
