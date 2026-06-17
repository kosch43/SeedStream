package torrent

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"seedstream/pkg/core/config"
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
	cfg      *config.Config
	checking atomic.Bool // guards against concurrent check runs
}

// NewWatchdog creates a Watchdog. Returns nil when the torrent manager has no
// configured clients, or when cerberus/indexer are nil (watchdog would be a
// no-op). cfg is used to look up per-tracker H&R rules at check time.
func NewWatchdog(m *Manager, cer *cerberus.Client, idx indexer.Indexer, cfg *config.Config) *Watchdog {
	if m == nil || !m.Enabled() || cer == nil || idx == nil {
		return nil
	}
	return &Watchdog{manager: m, cerberus: cer, indexer: idx, cfg: cfg}
}

// hnrRulesFor returns the H&R rules configured for the named indexer, or nil
// if none are set.
func (w *Watchdog) hnrRulesFor(indexerName string) *HnRRules {
	if w.cfg == nil || indexerName == "" {
		return nil
	}
	for _, idx := range w.cfg.Indexers {
		if !strings.EqualFold(idx.Name, indexerName) {
			continue
		}
		if idx.HnRMinSeedHours <= 0 && idx.HnRMinRatio <= 0 {
			return nil
		}
		return &HnRRules{
			MinSeedHours: idx.HnRMinSeedHours,
			MinRatio:     idx.HnRMinRatio,
			Mode:         idx.HnRMode,
		}
	}
	return nil
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
			// Skip this tick if the previous check is still running (e.g.
			// qBittorrent or Torznab search is slow). Prevents concurrent
			// checks from double-replacing the same torrent.
			if !w.checking.CompareAndSwap(false, true) {
				logger.Debug("Cerberus watchdog: previous check still running, skipping tick")
				continue
			}
			go func() {
				defer w.checking.Store(false)
				w.check(ctx, threshold)
			}()
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
			"progress", e.Progress,
			"imdb_id", rec.ImdbID, "tmdb_id", rec.TmdbID)
		w.handleStalled(ctx, e, rec)
	}
}

func isStalled(e TorrentHealthEntry, threshold time.Duration) bool {
	if e.Progress >= 0.999 {
		return false // complete, not stalled
	}
	// qBittorrent returns last_activity=0 for torrents that have never had
	// peer contact (e.g. freshly added). Treat zero as "no data yet" and
	// skip stall detection to avoid false positives on brand-new torrents.
	hasActivityData := e.LastActivity.Unix() > 0
	switch e.State {
	case "missingFiles", "error":
		// Only act on these after the torrent has had a chance to start.
		return hasActivityData && time.Since(e.AddedAt) > threshold
	case "stalledDL":
		return hasActivityData && time.Since(e.LastActivity) > threshold
	case "downloading":
		// Allow double the threshold for active-but-slow downloads.
		return hasActivityData && time.Since(e.LastActivity) > threshold*2
	}
	return false
}

func (w *Watchdog) handleStalled(ctx context.Context, stalled TorrentHealthEntry, rec *cerberus.TorrentRecord) {
	// If the torrent has downloaded any data, deleting it would leave the
	// tracker with an unmet seeding obligation (H&R on private trackers).
	// Re-announce to find new peers without touching the downloaded data.
	// Do NOT add to the blocklist — the torrent itself isn't bad, the swarm
	// just temporarily ran out of seeds.
	if stalled.Progress > 0 {
		rules := w.hnrRulesFor(rec.IndexerName)
		if rules != nil {
			// Check current seeding status against the tracker's H&R rules.
			if status, err := w.manager.GetSeedingStatus(ctx, stalled.Hash, stalled.ClientName); err == nil {
				safe := rules.Satisfied(status.SeedingHours, status.Ratio)
				logger.Info("Cerberus watchdog: partial torrent H&R status",
					"hash", stalled.Hash, "name", stalled.Name,
					"indexer", rec.IndexerName,
					"seeding_hours", status.SeedingHours,
					"ratio", status.Ratio,
					"min_seed_hours", rules.MinSeedHours,
					"min_ratio", rules.MinRatio,
					"mode", rules.Mode,
					"hnr_safe", safe,
				)
			}
		}
		logger.Info("Cerberus watchdog: re-announcing partial torrent (preserving seeding ratio)",
			"hash", stalled.Hash, "name", stalled.Name, "progress", stalled.Progress,
			"indexer", rec.IndexerName)
		if err := w.manager.Reannounce(ctx, stalled.ClientName, stalled.Hash); err != nil {
			logger.Warn("Cerberus watchdog: reannounce failed", "hash", stalled.Hash, "err", err)
		}
		return
	}

	// Progress == 0: nothing downloaded, no ratio obligation. Report the
	// failure, then find and swap in a better torrent.
	if err := w.cerberus.ReportFailure(stalled.Hash, "stalled: "+stalled.State); err != nil {
		logger.Warn("Cerberus watchdog: failed to report failure", "hash", stalled.Hash, "err", err)
	}

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
		logger.Error("Cerberus watchdog: replace failed, attempting to restore original torrent",
			"hash", stalled.Hash, "name", stalled.Name, "err", err)
		// Delete may have already succeeded before Add failed. Restore the original
		// torrent so it isn't silently lost from qBittorrent (Progress==0 so no data risk).
		if rec.Magnet != "" {
			if rerr := w.manager.AddTorrent(ctx, stalled.ClientName, rec.Magnet); rerr != nil {
				logger.Error("Cerberus watchdog: could not restore original torrent after failed replace",
					"hash", stalled.Hash, "err", rerr)
			} else {
				logger.Info("Cerberus watchdog: restored original torrent after failed replace", "hash", stalled.Hash)
			}
		}
		return
	}

	logger.Info("Cerberus watchdog: replaced zero-progress stalled torrent",
		"old_hash", stalled.Hash, "name", stalled.Name,
		"imdb_id", rec.ImdbID, "tmdb_id", rec.TmdbID)

	// Register the replacement's hash so that if it also stalls the watchdog
	// can identify its content and re-replace it.
	if newHash := InfoHashFromMagnet(newURL); newHash != "" {
		if err := w.cerberus.RegisterTorrent(newHash, ids, newURL, rec.ReleaseTitle, rec.IndexerName); err != nil {
			logger.Warn("Cerberus watchdog: failed to register replacement hash", "hash", newHash, "err", err)
		}
	} else {
		// HTTP .torrent URLs have no extractable hash — the replacement won't be
		// tracked by Cerberus and a future stall on it will be ignored.
		logger.Warn("Cerberus watchdog: replacement URL is not a magnet, hash cannot be registered — future stalls on this torrent will not be auto-replaced",
			"url", newURL, "name", stalled.Name)
	}
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
		if magnet != "" {
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

// normalizeInfoHash converts any BitTorrent info hash to lowercase hex (40 chars).
// Magnet URIs may carry the hash as 32-char base32 instead of 40-char hex.
func normalizeInfoHash(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) == 40 {
		return h
	}
	if len(h) == 32 {
		decoded, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded)
		}
	}
	return h
}

// InfoHashFromMagnet extracts and normalizes the BitTorrent info hash from a
// magnet URI. Returns an empty string if the URI is not a magnet or has no btih.
func InfoHashFromMagnet(magnet string) string {
	if !strings.HasPrefix(strings.ToLower(magnet), "magnet:") {
		return ""
	}
	u, err := url.Parse(magnet)
	if err != nil {
		return ""
	}
	for _, xt := range u.Query()["xt"] {
		lower := strings.ToLower(xt)
		if strings.HasPrefix(lower, "urn:btih:") {
			raw := strings.TrimPrefix(lower, "urn:btih:")
			return normalizeInfoHash(raw)
		}
	}
	return ""
}
