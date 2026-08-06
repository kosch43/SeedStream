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
	"seedstream/pkg/search/triage"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/services/uploadguard"
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
	meter    *uploadguard.Meter // monthly upload meter; nil disables metering
	checking atomic.Bool        // guards against concurrent check runs
}

// NewWatchdog creates a Watchdog. Returns nil when the torrent manager has no
// configured clients, or when cerberus/indexer are nil (watchdog would be a
// no-op). cfg is used to look up per-tracker H&R rules at check time. meter, if
// non-nil, is updated each check with the seedbox's BitTorrent upload so the
// monthly upload guard stays current.
func NewWatchdog(m *Manager, cer *cerberus.Client, idx indexer.Indexer, cfg *config.Config, meter *uploadguard.Meter) *Watchdog {
	if m == nil || !m.Enabled() || cer == nil || idx == nil {
		return nil
	}
	return &Watchdog{manager: m, cerberus: cer, indexer: idx, cfg: cfg, meter: meter}
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

	// Prune stale registry entries once at startup to keep the DB bounded.
	// 90 days is conservative: private-tracker H&R windows rarely exceed 30 days.
	if n := w.cerberus.PruneOldEntries(90); n > 0 {
		logger.Info("Cerberus watchdog: pruned old registry entries at startup", "count", n)
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
	w.syncUploadMeter(ctx)

	entries, err := w.manager.ListAll(ctx)
	if err != nil {
		logger.Warn("Cerberus watchdog: failed to list torrents", "err", err)
		return
	}
	for _, e := range entries {
		// A paused/stopped torrent is not seeding and not downloading. On a
		// completed torrent that is an active H&R risk on a private tracker; on
		// an incomplete one it just stops making progress. Either way SeedStream
		// added it to keep seeding, so resume it. This runs regardless of
		// progress and before the stall checks below.
		if isPausedState(e.State) {
			w.handlePaused(ctx, e)
			continue
		}
		if e.Progress >= 0.999 {
			// Completed torrents normally need no action. But error/missingFiles
			// states on a finished download warrant an H&R warning log.
			if e.State == "missingFiles" || e.State == "error" {
				w.checkCompletedError(ctx, e)
			}
			continue
		}
		underSeeded := isUnderSeeded(e, w.minSeeders(), stallThreshold)
		if !isStalled(e, stallThreshold) && !underSeeded {
			continue
		}
		// Already superseded on an earlier check. Nothing is ever deleted, so the
		// old torrent stays in the client; without this it would be re-diagnosed
		// and replaced again on every tick, adding a duplicate each time.
		if w.cerberus.IsBlocked(e.Hash) {
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

// syncUploadMeter folds the seedbox's BitTorrent upload into the monthly upload
// guard and logs when the cap has been reached. No-op when metering is off or
// the guard is disabled in config.
func (w *Watchdog) syncUploadMeter(ctx context.Context) {
	if w.meter == nil || !w.cfg.UploadGuardEnabled() {
		return
	}
	w.meter.SetLimits(w.cfg.MonthlyUploadCapBytes(), w.cfg.EffectiveUploadCapResetDay())
	w.meter.RecordSeedingTotals(w.manager.SeedingUploadTotals(ctx))

	used, capBytes := w.meter.Used(), w.meter.Cap()
	if w.meter.Throttled() {
		logger.Warn("Upload guard: monthly upload allowance reached — heavy titles are held until reset",
			"used_gb", float64(used)/1e9, "cap_gb", float64(capBytes)/1e9,
			"post_cap_mbps", w.cfg.EffectivePostCapUploadMbps(), "reset_day", w.cfg.EffectiveUploadCapResetDay())
	} else {
		logger.Debug("Upload guard: monthly upload usage",
			"used_gb", float64(used)/1e9, "cap_gb", float64(capBytes)/1e9)
	}
}

// checkCompletedError logs H&R risk when a fully-downloaded torrent enters an
// error or missingFiles state. It never deletes or replaces anything; it only
// emits a warning so the operator knows to act.
func (w *Watchdog) checkCompletedError(ctx context.Context, e TorrentHealthEntry) {
	rec := w.cerberus.GetContentByHash(e.Hash)
	if rec == nil {
		return
	}
	rules := w.hnrRulesFor(rec.IndexerName)
	if rules == nil {
		logger.Warn("Cerberus watchdog: completed torrent entered error state",
			"hash", e.Hash, "name", e.Name, "state", e.State, "indexer", rec.IndexerName)
		return
	}
	status, err := w.manager.GetSeedingStatus(ctx, e.Hash, e.ClientName)
	if err != nil {
		logger.Warn("Cerberus watchdog: completed torrent entered error state (H&R status unknown)",
			"hash", e.Hash, "name", e.Name, "state", e.State,
			"indexer", rec.IndexerName, "err", err)
		return
	}
	hnrSafe := rules.Satisfied(status.SeedingHours, status.Ratio)
	if !hnrSafe {
		logger.Warn("Cerberus watchdog: completed torrent in error state — H&R NOT satisfied, manual intervention required",
			"hash", e.Hash, "name", e.Name, "state", e.State,
			"indexer", rec.IndexerName,
			"seeding_hours", status.SeedingHours, "min_seed_hours", rules.MinSeedHours,
			"ratio", status.Ratio, "min_ratio", rules.MinRatio, "mode", rules.Mode)
	} else {
		logger.Info("Cerberus watchdog: completed torrent in error state (H&R satisfied)",
			"hash", e.Hash, "name", e.Name, "state", e.State,
			"indexer", rec.IndexerName,
			"seeding_hours", status.SeedingHours, "ratio", status.Ratio)
	}
}

// isPausedState reports whether a qBittorrent state means the torrent is
// paused/stopped and therefore neither seeding nor downloading. qBittorrent 5.0
// renamed the "paused*" states to "stopped*"; both spellings are covered.
func isPausedState(state string) bool {
	switch state {
	case "pausedUP", "pausedDL", "stoppedUP", "stoppedDL":
		return true
	}
	return false
}

// minSeeders is the configured swarm-size floor, or 0 when the check is off.
func (w *Watchdog) minSeeders() int {
	if w == nil || w.cfg == nil {
		return 0
	}
	return w.cfg.EffectiveMinSeeders()
}

// isUnderSeeded reports whether an unfinished torrent's swarm is too thin to
// carry it to completion, so the watchdog should act on it.
//
// Swarm size is predictive in a way that inactivity alone is not: a torrent with
// two seeders may still be trickling bytes, yet it will never keep ahead of
// playback. Rather than wait out the full stall threshold, a thin swarm is acted
// on at half of it — and only after the torrent has had time to find peers,
// since a freshly added torrent legitimately reports zero seeds for a few
// seconds. What happens next is the ordinary stall handling: a torrent with
// progress is only re-announced (never deleted, so seeding obligations survive),
// while one with nothing downloaded is swapped for a better-seeded release.
func isUnderSeeded(e TorrentHealthEntry, minSeeders int, threshold time.Duration) bool {
	if minSeeders <= 0 || e.Progress >= 0.999 {
		return false
	}
	if time.Since(e.AddedAt) < seedCheckGrace {
		return false
	}
	if e.NumSeeds >= minSeeders {
		return false
	}
	if e.LastActivity.Unix() <= 0 {
		// Never had peer contact at all: judge from when it was added.
		return time.Since(e.AddedAt) > threshold
	}
	return time.Since(e.LastActivity) > threshold/2
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
	case "metaDL":
		// Magnet stuck fetching metadata — no peers are supplying the .torrent.
		// last_activity stays 0 here, so gate on time since it was added.
		return time.Since(e.AddedAt) > threshold
	case "stalledDL":
		return hasActivityData && time.Since(e.LastActivity) > threshold
	case "downloading", "forcedDL":
		// forcedDL is "downloading" with queueing bypassed — force-started
		// torrents can stall exactly like normal ones, and omitting the state
		// meant the watchdog never looked at them at all.
		// Allow double the threshold for active-but-slow downloads.
		return hasActivityData && time.Since(e.LastActivity) > threshold*2
	}
	return false
}

// handlePaused resumes a paused/stopped torrent so it seeds (or resumes
// downloading). Best-effort and idempotent — resuming an already-running
// torrent is harmless. It never deletes data, so it is always ratio-safe.
func (w *Watchdog) handlePaused(ctx context.Context, e TorrentHealthEntry) {
	rec := w.cerberus.GetContentByHash(e.Hash)
	indexerName := ""
	if rec != nil {
		indexerName = rec.IndexerName
	}
	complete := e.Progress >= 0.999
	logger.Warn("Cerberus watchdog: torrent paused — resuming to keep it seeding",
		"hash", e.Hash, "name", e.Name, "state", e.State,
		"progress", e.Progress, "complete", complete, "indexer", indexerName)
	if err := w.manager.Resume(ctx, e.ClientName, e.Hash); err != nil {
		logger.Warn("Cerberus watchdog: failed to resume paused torrent", "hash", e.Hash, "err", err)
	}
}

func (w *Watchdog) handleStalled(ctx context.Context, stalled TorrentHealthEntry, rec *cerberus.TorrentRecord) {
	// missingFiles means the torrent's data was deleted from disk externally.
	// Re-announcing cannot recover lost files; the only safe action is to log
	// an H&R warning for the operator to act on manually.
	if stalled.State == "missingFiles" && stalled.Progress > 0 {
		hnrSafe := true
		if rules := w.hnrRulesFor(rec.IndexerName); rules != nil {
			if status, err := w.manager.GetSeedingStatus(ctx, stalled.Hash, stalled.ClientName); err == nil {
				hnrSafe = rules.Satisfied(status.SeedingHours, status.Ratio)
			}
		}
		logger.Warn("Cerberus watchdog: torrent files missing from disk — cannot auto-recover",
			"hash", stalled.Hash, "name", stalled.Name, "progress", stalled.Progress,
			"indexer", rec.IndexerName, "hnr_safe", hnrSafe)
		return
	}

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

	// Adds the alternative alongside the stalled torrent; nothing is removed, so
	// a failure here leaves the original exactly as it was and needs no recovery.
	if err := w.manager.Replace(ctx, stalled.ClientName, stalled.Hash, newURL); err != nil {
		logger.Error("Cerberus watchdog: could not add replacement, original torrent left untouched",
			"hash", stalled.Hash, "name", stalled.Name, "err", err)
		return
	}

	logger.Info("Cerberus watchdog: added a healthier alternative for a zero-progress stalled torrent (nothing deleted)",
		"old_hash", stalled.Hash, "name", stalled.Name,
		"imdb_id", rec.ImdbID, "tmdb_id", rec.TmdbID)

	// Register the replacement's hash so that if it also stalls the watchdog
	// can identify its content and re-replace it.
	if newHash := InfoHashFromMagnet(newURL); newHash != "" {
		if err := w.cerberus.RegisterTorrent(newHash, ids, newURL, rec.ReleaseTitle, rec.IndexerName); err != nil {
			logger.Warn("Cerberus watchdog: failed to register replacement hash", "hash", newHash, "err", err)
		}
		// Keep qBittorrent from ending this one's seeding on its own either.
		w.manager.ProtectSeeding(ctx, stalled.ClientName, newHash)
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

	// A replacement is ranked exactly as the stream list ranks releases: quality
	// first, with swarm health folded into the same score. Ordering by seeder
	// count alone would happily swap a stalled remux for a tiny well-seeded rip.
	type candidate struct {
		url     string
		score   int
		seeders int
		dead    bool
	}
	minSeeders := w.minSeeders()
	var candidates []candidate
	for i := range resp.Channel.Items {
		item := &resp.Channel.Items[i]
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
		if magnet == "" {
			continue
		}
		rel := item.ToRelease()
		if rel == nil {
			continue
		}
		// Never replace a stalled torrent with one too thinly seeded to finish,
		// which would just stall again on the next check.
		if minSeeders > 0 && rel.SeedersKnown && rel.Seeders < minSeeders {
			continue
		}
		candidates = append(candidates, candidate{
			url:     magnet,
			score:   triage.ScoreRelease(rel),
			seeders: rel.Seeders,
			dead:    rel.SeedersKnown && rel.Seeders <= 0,
		})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dead != candidates[j].dead {
			return candidates[j].dead // anything playable before a dead swarm
		}
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].seeders > candidates[j].seeders
	})
	logger.Debug("Cerberus watchdog: replacement chosen",
		"candidates", len(candidates), "score", candidates[0].score, "seeders", candidates[0].seeders)
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
