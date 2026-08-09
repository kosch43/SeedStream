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
	"seedstream/pkg/services/uploadguard"
	"seedstream/pkg/torrent/tclient"
)

const (
	defaultStallThresholdMinutes = 10
	defaultCheckIntervalMinutes  = 3
	// streamingOrderMinDownloadedPieces is how many later pieces of the selected
	// video must already be on disk before its missing first piece proves the
	// download is not happening from that file's front.
	streamingOrderMinDownloadedPieces = 5
)

// WatchdogConfig controls Cerberus watchdog behavior.
type WatchdogConfig struct {
	// StallThresholdMinutes is how long a torrent must be inactive before it
	// is considered stalled. Default 10 minutes.
	StallThresholdMinutes int
	// CheckIntervalMinutes is how often the watchdog polls qBittorrent.
	// Default 3 minutes.
	CheckIntervalMinutes int
	// ReplaceStalled allows the watchdog to add a healthier alternative when a
	// torrent stalls at zero progress. It defaults to false: a download that
	// cannot start is left alone rather than answered with a second copy of the
	// same title.
	//
	// The replacement is never a swap — Replace adds alongside and deletes
	// nothing, deliberately, because removing a torrent already announced to a
	// private tracker risks a hit-and-run. That safety property is exactly what
	// makes the behaviour expensive: every stall that resolves this way leaves
	// another torrent on the seedbox for good, and if the replacement also
	// stalls the next tick can add another. On a shared or quota-limited box
	// that accumulates without bound, so it is opt-in.
	ReplaceStalled bool
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
	// replaceStalled mirrors WatchdogConfig.ReplaceStalled. Written once in
	// Start before the ticker loop begins, so later reads from a check are
	// ordered after it.
	replaceStalled bool
	// headWarned records which torrents have already been reported for a
	// missing head piece, so a torrent whose picker ignores the ordering flags
	// warns once instead of on every tick.
	headWarned map[string]bool
	// lastRetentionReview paces the dry-run obligation report; obligations are
	// measured in days so there is nothing to gain from reporting every tick.
	lastRetentionReview time.Time
	diskPressure        map[string]bool
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
	return &Watchdog{
		manager:      m,
		cerberus:     cer,
		indexer:      idx,
		cfg:          cfg,
		meter:        meter,
		headWarned:   make(map[string]bool),
		diskPressure: make(map[string]bool),
	}
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
	w.replaceStalled = cfg.ReplaceStalled

	// Prune stale registry entries once at startup to keep the DB bounded.
	// 90 days is conservative: private-tracker H&R windows rarely exceed 30 days.
	if n := w.cerberus.PruneOldEntries(90); n > 0 {
		logger.Info("Cerberus watchdog: pruned old registry entries at startup", "count", n)
	}
	// Expire stale blocklist entries so a swarm that has since recovered is not
	// shut out forever.
	w.cerberus.PruneBlocklist(w.cfg.EffectiveCerberusBlocklistDays())

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

// allowCleanupFor reports whether the operator has explicitly opted this tracker
// in to cleanup consideration. Absent means no.
func (w *Watchdog) allowCleanupFor(indexerName string) bool {
	if w.cfg == nil || indexerName == "" {
		return false
	}
	for _, idx := range w.cfg.Indexers {
		if strings.EqualFold(idx.Name, indexerName) {
			return idx.HnRAllowCleanup != nil && *idx.HnRAllowCleanup
		}
	}
	return false
}

// reviewHnRRisk warns about obligations heading toward a breach while there is
// still time to act. Seed time on its own cannot say whether a torrent is in
// trouble; measuring it against the tracker's deadline can.
func (w *Watchdog) reviewHnRRisk(entries []TorrentHealthEntry) {
	now := time.Now()
	for _, e := range entries {
		rec := w.cerberus.GetContentByHash(e.Hash)
		if rec == nil {
			continue
		}
		st := EvaluateHnR(e, rec.IndexerName, w.hnrRulesFor(rec.IndexerName),
			w.hnrWindowDaysFor(rec.IndexerName), now)
		switch st.Risk {
		case HnRRiskCritical:
			logger.Warn("Cerberus H&R: ACT NOW — obligation about to be breached",
				"name", st.Name, "hash", shortHash(st.Hash), "indexer", st.IndexerName,
				"detail", st.Detail, "hours_remaining", st.HoursRemaining)
		case HnRRiskWarning:
			logger.Warn("Cerberus H&R: obligation at risk",
				"name", st.Name, "hash", shortHash(st.Hash), "indexer", st.IndexerName,
				"detail", st.Detail, "hours_remaining", st.HoursRemaining)
		case HnRRiskWatch:
			logger.Info("Cerberus H&R: past half the tracker's window",
				"name", st.Name, "hash", shortHash(st.Hash), "indexer", st.IndexerName,
				"detail", st.Detail)
		}
	}
}

// hnrWindowDaysFor returns the tracker's deadline in days, 0 when unknown.
func (w *Watchdog) hnrWindowDaysFor(indexerName string) float64 {
	if w.cfg == nil || indexerName == "" {
		return 0
	}
	for _, idx := range w.cfg.Indexers {
		if strings.EqualFold(idx.Name, indexerName) {
			return idx.HnRWindowDays
		}
	}
	return 0
}

// reviewRetention reports which completed torrents have provably discharged
// their tracker obligations. It is a dry run and deletes nothing — there is no
// removal path anywhere in SeedStream. Its purpose is to let an operator compare
// these verdicts against what their trackers actually show, so that a future
// cleanup feature can be judged on evidence rather than on trust in local
// counters.
func (w *Watchdog) reviewRetention(ctx context.Context, entries []TorrentHealthEntry) {
	margin := w.cfg.EffectiveHnRSafetyMarginPercent()
	eligible, reviewed := 0, 0
	for _, e := range entries {
		if e.Progress < 0.999 {
			continue
		}
		rec := w.cerberus.GetContentByHash(e.Hash)
		if rec == nil {
			continue
		}
		if !w.allowCleanupFor(rec.IndexerName) {
			continue // never even evaluate a tracker that was not opted in
		}
		reviewed++
		v := w.manager.EvaluateRetention(ctx, e, rec.IndexerName,
			w.hnrRulesFor(rec.IndexerName), true, margin)
		if v.Eligible {
			eligible++
			logger.Info("Cerberus retention review (DRY RUN — nothing is deleted): obligations appear met",
				"name", v.Name, "hash", shortHash(v.Hash), "indexer", v.IndexerName,
				"seeded_hours", v.SeedingHours, "required_hours", v.RequiredHours,
				"ratio", v.Ratio, "required_ratio", v.RequiredRatio,
				"tracker_answering", v.TrackerWorking, "verdict", v.Reason)
		} else {
			logger.Debug("Cerberus retention review (DRY RUN): still owed",
				"name", v.Name, "hash", shortHash(v.Hash), "indexer", v.IndexerName,
				"reason", v.Reason)
		}
	}
	if reviewed > 0 {
		logger.Info("Cerberus retention review complete (DRY RUN — no torrent was removed)",
			"reviewed", reviewed, "would_be_eligible", eligible,
			"safety_margin_percent", margin)
	}
}

func (w *Watchdog) check(ctx context.Context, stallThreshold time.Duration) {
	w.syncUploadMeter(ctx)

	entries, err := w.manager.ListAll(ctx)
	if err != nil {
		logger.Warn("Cerberus watchdog: failed to list torrents", "err", err)
		return
	}
	diskPressure := w.enforceDiskGuard(ctx, entries)

	// Obligations heading toward a breach are worth knowing about promptly, so
	// this runs every check rather than on the slower retention cadence.
	w.reviewHnRRisk(entries)

	// Periodic dry run: report which obligations look discharged, without acting.
	if time.Since(w.lastRetentionReview) >= retentionReviewInterval {
		w.lastRetentionReview = time.Now()
		w.reviewRetention(ctx, entries)
	}
	for _, e := range entries {
		// A paused/stopped torrent is not seeding and not downloading. On a
		// completed torrent that is an active H&R risk on a private tracker; on
		// an incomplete one it just stops making progress. Either way SeedStream
		// added it to keep seeding, so resume it. This runs regardless of
		// progress and before the stall checks below.
		if isPausedState(e.State) {
			if diskPressure[e.ClientName] {
				// The disk guard paused this torrent, so do not let the normal
				// H&R safety resume path undo the emergency stop.
				continue
			}
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

	// Streaming order is asserted at add time and again at playback time, but
	// both are moments. Read the flags back from the API every check and repair
	// them, then confirm that each video qBittorrent currently marks as a
	// playback priority has its real first piece on disk.
	w.verifyStreamingOrder(ctx, entries)
}

// verifyStreamingOrder makes every incomplete SeedStream torrent download from
// the front and checks that the ordering actually works rather than merely
// being switched on.
//
// For a multi-file torrent, torrent-global piece 0 may be a sample, an earlier
// episode, or another unrelated file. Cerberus therefore observes the video
// files qBittorrent currently reports at playback priority and checks each
// file's PieceRange[0]. It never selects a file from persisted content metadata:
// that record can lag behind an episode which is still preparing.
//
// The piece bitmap is the only API view that says WHERE downloaded bytes are.
// If the selected first piece is not downloaded while later pieces of the
// same file have completed, the viewer's head buffer cannot fill no matter how
// much aggregate progress qBittorrent reports. That condition is reported once
// per client/torrent/file; streaming-order flags are repaired on every check.
func (w *Watchdog) verifyStreamingOrder(ctx context.Context, entries []TorrentHealthEntry) {
	seenIncomplete := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Progress >= 1 {
			continue
		}

		orderMissing := e.StreamingOrderSupported && (!e.SequentialDL || !e.FirstLastPiecePrio)
		if orderMissing {
			logger.Warn("Cerberus watchdog: torrent is not set to download from the front — enabling sequential download and first/last-piece priority",
				"hash", e.Hash, "name", e.Name, "client", e.ClientName,
				"state", e.State, "progress", e.Progress,
				"sequential_dl", e.SequentialDL, "first_last_piece_prio", e.FirstLastPiecePrio)
			if err := w.manager.EnsureStreamingOrder(ctx, e.ClientName, e.Hash); err != nil {
				logger.Warn("Cerberus watchdog: could not enable streaming order",
					"hash", e.Hash, "name", e.Name, "client", e.ClientName, "err", err)
			}
		}

		heads, err := w.manager.StreamingHeads(ctx, e.ClientName, e.Hash)
		if err != nil {
			logger.Warn("Cerberus watchdog: could not verify the selected file's head",
				"hash", e.Hash, "client", e.ClientName, "err", err)
			continue
		}
		for _, head := range heads {
			key := streamingHeadKey(e.ClientName, e.Hash, head.FileIndex)
			seenIncomplete[key] = true
			// The flags were just repaired. Give the picker a check interval
			// before judging work requested under the old order.
			if orderMissing {
				continue
			}
			if !headPieceMissing(head.FirstPieceState, head.DownloadedPiecesAfter) {
				delete(w.headWarned, key)
				continue
			}
			if !w.headWarned[key] {
				w.headWarned[key] = true
				logger.Warn("Cerberus watchdog: selected video's first piece is not downloaded while later pieces are complete",
					"hash", e.Hash, "name", e.Name, "client", e.ClientName,
					"state", e.State, "progress", e.Progress,
					"file", head.FileName, "file_index", head.FileIndex,
					"first_piece", head.FirstPiece, "first_piece_state", head.FirstPieceState,
					"later_file_pieces_downloaded", head.DownloadedPiecesAfter)
				// Reassert streaming flags, re-anchor sequential download to the
				// missing head piece (Transmission 4.1+), and reannounce to find
				// a peer that carries the data.
				if err := w.manager.EnsureStreamingOrder(ctx, e.ClientName, e.Hash); err != nil {
					logger.Debug("Cerberus watchdog: could not reassert streaming order for missing head",
						"hash", e.Hash, "client", e.ClientName, "err", err)
				}
				if err := w.manager.SteerPiece(ctx, e.ClientName, e.Hash, head.FirstPiece); err != nil {
					logger.Debug("Cerberus watchdog: could not re-anchor sequential download to the missing head piece",
						"hash", e.Hash, "client", e.ClientName, "piece", head.FirstPiece, "err", err)
				}
				if err := w.manager.Reannounce(ctx, e.ClientName, e.Hash); err != nil {
					logger.Debug("Cerberus watchdog: could not reannounce torrent with missing head",
						"hash", e.Hash, "client", e.ClientName, "err", err)
				}
			}
		}
	}
	// Forget warnings for torrents that left the category so the map cannot
	// grow without bound across the client's lifetime.
	for h := range w.headWarned {
		if !seenIncomplete[h] {
			delete(w.headWarned, h)
		}
	}
}

func streamingHeadKey(clientName, hash string, fileIndex int) string {
	return strings.TrimSpace(clientName) + ":" + strings.ToLower(strings.TrimSpace(hash)) + ":" + strconv.Itoa(fileIndex)
}

// headPieceMissing reports whether the selected video's first piece is still
// unreadable despite several later pieces of that same file being complete.
func headPieceMissing(firstPieceState, downloadedAfter int) bool {
	return firstPieceState != tclient.PieceDownloaded &&
		downloadedAfter >= streamingOrderMinDownloadedPieces
}

// enforceDiskGuard pauses only torrents belonging to a client whose local
// SavePath is above the configured threshold. A five-point recovery buffer is
// applied by the config layer so a full disk cannot cause pause/resume flapping.
func (w *Watchdog) enforceDiskGuard(ctx context.Context, entries []TorrentHealthEntry) map[string]bool {
	pressure := make(map[string]bool)
	if w.cfg == nil {
		w.diskPressure = pressure
		return pressure
	}
	threshold := w.cfg.EffectiveDiskGuardThresholdPercent()
	if threshold <= 0 {
		w.diskPressure = pressure
		return pressure
	}
	recovery := w.cfg.EffectiveDiskGuardRecoveryPercent()
	byClient := make(map[string][]TorrentHealthEntry)
	for _, e := range entries {
		byClient[e.ClientName] = append(byClient[e.ClientName], e)
	}
	for _, client := range w.manager.clients {
		path := strings.TrimSpace(client.cfg.SavePath)
		if path == "" {
			continue
		}
		used, err := diskUsagePercent(path)
		if err != nil {
			logger.Warn("Cerberus disk guard: could not inspect download filesystem",
				"client", client.cfg.Name, "path", path, "err", err)
			continue
		}
		wasPressure := w.diskPressure[client.cfg.Name]
		isPressure := used >= threshold || (wasPressure && used > recovery)
		if isPressure {
			pressure[client.cfg.Name] = true
		}
		if isPressure != wasPressure {
			if isPressure {
				logger.Warn("Cerberus disk guard: download filesystem is full; pausing SeedStream torrents",
					"client", client.cfg.Name, "path", path, "used_percent", used, "threshold_percent", threshold)
			} else {
				logger.Info("Cerberus disk guard: download filesystem recovered; torrents may resume",
					"client", client.cfg.Name, "path", path, "used_percent", used, "recovery_percent", recovery)
			}
		}
		if !isPressure {
			continue
		}
		for _, e := range byClient[client.cfg.Name] {
			// Completed torrents do not consume download space. Leave them
			// seeding so an emergency disk stop cannot create a needless H&R risk.
			if e.Progress >= 0.999 {
				continue
			}
			if isPausedState(e.State) {
				continue
			}
			if err := w.manager.Pause(ctx, e.ClientName, e.Hash); err != nil {
				logger.Warn("Cerberus disk guard: failed to pause torrent",
					"client", e.ClientName, "hash", e.Hash, "err", err)
			}
		}
	}
	w.diskPressure = pressure
	return pressure
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
	// The tracker's count is the one the operator's setting is expressed in.
	// Falling back to the connected count is unavoidable before the first
	// scrape, but it under-reports — BitTorrent connects to a subset of the
	// swarm — so on its own it would condemn healthy torrents and replace them
	// with worse ones.
	if e.SwarmKnown {
		if e.SwarmSeeds >= minSeeders {
			return false
		}
	} else if e.NumSeeds >= minSeeders {
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

	// Report and stop unless replacement was explicitly asked for. The failure
	// above is still recorded, so the dead swarm is blocklisted and will not be
	// chosen again; what is skipped is starting a second download of the same
	// title. A stream that cannot start is left as a failed stream.
	if !w.replaceStalled {
		logger.Info("Cerberus watchdog: zero-progress stall left alone (replacement disabled)",
			"hash", stalled.Hash, "name", stalled.Name, "state", stalled.State)
		return
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

	// A replacement is ranked on swarm size alone: most seeders first.
	//
	// The torrent being replaced is one that stalled, so the only question that
	// matters is which candidate will actually finish. Seeder count is the best
	// available answer to that, and it is the same measure the seeder floor is
	// expressed in. Quality ranked the stream list when the viewer picked from
	// it; here the release they picked has already failed, and preferring a
	// better-looking one that also cannot download just stalls again.
	//
	// The trade is real and deliberate: a stalled remux can be replaced by a
	// smaller, lower-quality release with a much healthier swarm.
	type candidate struct {
		url     string
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
		return candidates[i].seeders > candidates[j].seeders
	})
	logger.Debug("Cerberus watchdog: replacement chosen",
		"candidates", len(candidates), "seeders", candidates[0].seeders)
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
