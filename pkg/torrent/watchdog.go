package torrent

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/services/cerberus"
)

const (
	defaultStallThresholdMinutes = 10
	defaultCheckIntervalMinutes  = 3
)

// WatchdogConfig controls Cerberus watchdog behavior.
type WatchdogConfig struct {
	// StallThresholdMinutes is how long a torrent must be inactive before it
	// is considered stalled. Default 10 minutes.
	StallThresholdMinutes int
	// CheckIntervalMinutes is how often the watchdog polls qBittorrent.
	// Default 3 minutes.
	CheckIntervalMinutes int
}

// Watchdog monitors active torrents for stalls and automatically replaces
// them with healthier alternatives found via the Torznab aggregator.
type Watchdog struct {
	manager  *Manager
	cerberus *cerberus.Client
	indexer  indexer.Indexer
}

// NewWatchdog creates a Watchdog. Returns nil when the torrent manager has no
// configured clients, or when cerberus/indexer are nil (watchdog would be
// a no-op).
func NewWatchdog(m *Manager, cer *cerberus.Client, idx indexer.Indexer) *Watchdog {
	if m == nil || !m.Enabled() || cer == nil || idx == nil {
		return nil
	}
	return &Watchdog{manager: m, cerberus: cer, indexer: idx}
}

// Start runs the watchdog loop until ctx is cancelled. Intended to be called
// in a goroutine.
func (w *Watchdog) Start(ctx context.Context, cfg WatchdogConfig) {
	if w == nil {
		return
	}
	interval := time.Duration(cfg.CheckIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = time.Duration(defaultCheckIntervalMinutes) * time.Minute
	}
	threshold := time.Duration(cfg.StallThresholdMinutes) * time.Minute
	if threshold <= 0 {
		threshold = time.Duration(defaultStallThresholdMinutes) * time.Minute
	}

	logger.Info("Cerberus watchdog started", "check_interval", interval, "stall_threshold", threshold)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("Cerberus watchdog stopped")
			return
		case <-ticker.C:
			w.check(ctx, threshold)
		}
	}
}

func (w *Watchdog) check(ctx context.Context, stallThreshold time.Duration) {
	entries, err := w.manager.ListAll(ctx)
	if err != nil {
		logger.Warn("Cerberus watchdog: failed to list torrents", "err", err)
		return
	}
	for _, e := range entries {
		if !isStalled(e, stallThreshold) {
			continue
		}
		rec := w.cerberus.GetContentByHash(e.Hash)
		if rec == nil {
			// Torrent was not added by Cerberus-aware code; skip.
			continue
		}
		logger.Info("Cerberus watchdog: stalled torrent detected",
			"hash", e.Hash, "name", e.Name, "state", e.State,
			"imdb_id", rec.ImdbID, "tmdb_id", rec.TmdbID)
		if err := w.cerberus.ReportFailure(e.Hash, "stalled: "+e.State); err != nil {
			logger.Warn("Cerberus watchdog: failed to report failure", "hash", e.Hash, "err", err)
		}
		w.replaceStalled(ctx, e, rec)
	}
}

func isStalled(e TorrentHealthEntry, threshold time.Duration) bool {
	if e.Progress >= 0.999 {
		return false // complete, not stalled
	}
	switch e.State {
	case "missingFiles", "error":
		return true
	case "stalledDL":
		return time.Since(e.LastActivity) > threshold
	case "downloading":
		// Allow double the threshold for active-but-slow downloads.
		return time.Since(e.LastActivity) > threshold*2
	}
	return false
}

func (w *Watchdog) replaceStalled(ctx context.Context, stalled TorrentHealthEntry, rec *cerberus.TorrentRecord) {
	ids := cerberus.ContentIDs{
		ImdbID:  rec.ImdbID,
		TmdbID:  rec.TmdbID,
		TvdbID:  rec.TvdbID,
		Season:  rec.Season,
		Episode: rec.Episode,
	}
	blocked := w.cerberus.GetBlockedHashes(ids)
	blockedSet := make(map[string]bool, len(blocked)+1)
	for _, h := range blocked {
		blockedSet[strings.ToLower(h)] = true
	}

	newURL := w.findReplacement(rec, blockedSet)
	if newURL == "" {
		logger.Warn("Cerberus watchdog: no replacement found",
			"hash", stalled.Hash, "name", stalled.Name,
			"imdb_id", rec.ImdbID, "tmdb_id", rec.TmdbID)
		return
	}

	if err := w.manager.Replace(ctx, stalled.ClientName, stalled.Hash, newURL); err != nil {
		logger.Warn("Cerberus watchdog: replace failed",
			"hash", stalled.Hash, "err", err)
		return
	}
	logger.Info("Cerberus watchdog: replaced stalled torrent",
		"old_hash", stalled.Hash, "name", stalled.Name,
		"imdb_id", rec.ImdbID, "tmdb_id", rec.TmdbID)
}

func (w *Watchdog) findReplacement(rec *cerberus.TorrentRecord, blocked map[string]bool) string {
	req := indexer.SearchRequest{
		IMDbID:     rec.ImdbID,
		TMDBID:     rec.TmdbID,
		TVDBID:     rec.TvdbID,
		SearchMode: "id",
		Limit:      20,
	}
	if req.IMDbID == "" && req.TMDBID == "" && req.TVDBID == "" {
		return ""
	}
	if rec.Season > 0 {
		req.Season = strconv.Itoa(rec.Season)
	}
	if rec.Episode > 0 {
		req.Episode = strconv.Itoa(rec.Episode)
	}

	resp, err := w.indexer.Search(req)
	if err != nil || resp == nil {
		return ""
	}

	type candidate struct {
		url     string
		seeders int
	}
	var candidates []candidate
	for _, item := range resp.Channel.Items {
		if !item.IsTorrent() {
			continue
		}
		hash := strings.ToLower(item.GetAttribute("infohash"))
		if hash != "" && blocked[hash] {
			continue
		}
		magnet := item.GetAttribute("magneturl")
		if magnet == "" {
			magnet = item.Link
		}
		if strings.HasPrefix(strings.ToLower(magnet), "magnet:") || magnet != "" {
			seeders, _ := strconv.Atoi(item.GetAttribute("seeders"))
			candidates = append(candidates, candidate{url: magnet, seeders: seeders})
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].seeders > candidates[j].seeders
	})
	return candidates[0].url
}
