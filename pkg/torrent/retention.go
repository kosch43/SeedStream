package torrent

import (
	"context"
	"fmt"
	"time"

	"seedstream/pkg/torrent/qbittorrent"
)

// RetentionVerdict is the outcome of asking whether a torrent's tracker
// obligations are provably discharged.
//
// It is a report, not an instruction. Nothing in SeedStream deletes torrents,
// and this type exists so that decision can be observed and checked against what
// trackers actually show before any removal is ever built on top of it.
type RetentionVerdict struct {
	Hash        string
	Name        string
	IndexerName string

	// Eligible is true only when every check passed. It is false whenever
	// anything at all could not be confirmed.
	Eligible bool
	// Reason explains the verdict in one line, for the operator to compare
	// against the tracker's own view.
	Reason string

	SeedingHours   float64
	Ratio          float64
	RequiredHours  float64 // already includes the safety margin
	RequiredRatio  float64
	TrackerWorking bool
}

// EvaluateRetention decides whether a torrent's obligations to its tracker are
// provably met, erring toward "no" on every uncertainty.
//
// The checks are ordered so the cheapest disqualifiers run first, and each one
// fails closed:
//
//   - Rules must exist and be meaningful. An unmatched or unconfigured tracker
//     means the obligation is unknown, not absent. HnRRules.Satisfied treats nil
//     as satisfied because it is a warning helper; relying on that here would
//     mean every torrent from an unconfigured tracker looks instantly disposable.
//   - The tracker must be opted in explicitly, so this can never act on a tracker
//     the operator has not deliberately reviewed.
//   - The torrent must be complete. A partial download owes seeding it cannot
//     have performed.
//   - The tracker must be answering announces. Seeding time and ratio are counted
//     by qBittorrent locally, so a torrent whose announces are failing racks up
//     both while the tracker records nothing.
//   - Both thresholds must be met when both are set, regardless of the any/all
//     mode used for warnings, and seed time must clear its requirement by the
//     configured safety margin.
//
// Freshness matters as much as the rules: status is read here, at decision time,
// rather than reused from an earlier pass.
func (m *Manager) EvaluateRetention(
	ctx context.Context,
	e TorrentHealthEntry,
	indexerName string,
	rules *HnRRules,
	allowCleanup bool,
	safetyMarginPercent int,
) RetentionVerdict {
	v := RetentionVerdict{Hash: e.Hash, Name: e.Name, IndexerName: indexerName}

	if rules == nil || (rules.MinSeedHours <= 0 && rules.MinRatio <= 0) {
		v.Reason = "no hit-and-run rules configured for this tracker, so the obligation is unknown"
		return v
	}
	if !allowCleanup {
		v.Reason = "cleanup not enabled for this tracker"
		return v
	}
	if e.Progress < 0.999 {
		v.Reason = fmt.Sprintf("torrent is only %.2f%% complete", floorPercent(e.Progress))
		return v
	}

	c := m.clientByName(e.ClientName)
	if c == nil {
		v.Reason = "torrent client not found, cannot verify anything"
		return v
	}

	// Read status now rather than trusting anything gathered earlier.
	info, err := c.Get(ctx, e.Hash)
	if err != nil || info == nil {
		v.Reason = "could not read current seeding status from the client"
		return v
	}
	v.SeedingHours = float64(info.SeedingTime) / 3600
	v.Ratio = info.Ratio

	trackers, err := c.Trackers(ctx, e.Hash)
	if err != nil {
		v.Reason = "could not read tracker announce status"
		return v
	}
	v.TrackerWorking = anyTrackerWorking(trackers)
	if !v.TrackerWorking {
		v.Reason = "tracker is not answering announces, so locally counted seed time may never have reached it"
		return v
	}

	if safetyMarginPercent < 0 {
		safetyMarginPercent = 0
	}
	if rules.MinSeedHours > 0 {
		v.RequiredHours = rules.MinSeedHours * (1 + float64(safetyMarginPercent)/100)
		if v.SeedingHours < v.RequiredHours {
			v.Reason = fmt.Sprintf("seeded %.1fh of the %.1fh required (%.0fh plus a %d%% margin)",
				v.SeedingHours, v.RequiredHours, rules.MinSeedHours, safetyMarginPercent)
			return v
		}
	}
	if rules.MinRatio > 0 {
		v.RequiredRatio = rules.MinRatio
		if v.Ratio < v.RequiredRatio {
			v.Reason = fmt.Sprintf("ratio %.2f is below the required %.2f", v.Ratio, v.RequiredRatio)
			return v
		}
	}

	v.Eligible = true
	v.Reason = fmt.Sprintf("obligations met: seeded %.1fh (needed %.1fh) at ratio %.2f, tracker answering",
		v.SeedingHours, v.RequiredHours, v.Ratio)
	return v
}

// anyTrackerWorking reports whether at least one real tracker is currently
// answering. DHT, PeX and LSD appear in the same list as disabled pseudo-entries
// and are ignored, since they say nothing about the private tracker's view.
func anyTrackerWorking(trackers []qbittorrent.TrackerInfo) bool {
	for _, t := range trackers {
		if t.Status == qbittorrent.TrackerWorking {
			return true
		}
	}
	return false
}

// retentionReviewInterval is how often the dry run reports. Obligations are
// measured in days, so re-reporting every watchdog tick would be noise.
const retentionReviewInterval = 6 * time.Hour
