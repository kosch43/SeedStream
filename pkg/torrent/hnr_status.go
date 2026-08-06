package torrent

import (
	"fmt"
	"time"
)

// HnRRisk describes how close a torrent is to breaching its tracker obligation.
type HnRRisk string

const (
	// HnRRiskUnknown: no rules configured, so nothing can be said.
	HnRRiskUnknown HnRRisk = "unknown"
	// HnRRiskMet: the obligation is discharged.
	HnRRiskMet HnRRisk = "met"
	// HnRRiskOK: still owed, but comfortably inside the window.
	HnRRiskOK HnRRisk = "ok"
	// HnRRiskWatch: past half the window with the obligation unmet.
	HnRRiskWatch HnRRisk = "watch"
	// HnRRiskWarning: little of the window left.
	HnRRiskWarning HnRRisk = "warning"
	// HnRRiskCritical: about to breach, or already past the deadline.
	HnRRiskCritical HnRRisk = "critical"
)

// HnRStatus is a torrent's standing against its tracker's hit-and-run rules.
type HnRStatus struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	IndexerName string  `json:"indexer_name"`
	Risk        HnRRisk `json:"risk"`
	Detail      string  `json:"detail"`

	Complete      bool    `json:"complete"`
	SeedingHours  float64 `json:"seeding_hours"`
	RequiredHours float64 `json:"required_hours"`
	Ratio         float64 `json:"ratio"`
	RequiredRatio float64 `json:"required_ratio"`
	Satisfied     bool    `json:"satisfied"`

	// WindowDays is the tracker's deadline, 0 when unknown.
	WindowDays float64 `json:"window_days"`
	// HoursRemaining until the deadline; negative once past it. Only meaningful
	// when WindowDays is set.
	HoursRemaining float64 `json:"hours_remaining"`
	// WindowKnown distinguishes "plenty of time" from "no deadline configured",
	// which look identical in the numbers but mean very different things.
	WindowKnown bool `json:"window_known"`
}

// Urgent reports whether this status deserves the operator's attention now.
func (s HnRStatus) Urgent() bool {
	return s.Risk == HnRRiskWarning || s.Risk == HnRRiskCritical
}

// EvaluateHnR reports a torrent's standing against its tracker's rules.
//
// Seed time alone cannot tell you whether you are in trouble: forty hours of a
// seventy-two hour obligation is comfortable with twelve days left and an
// emergency with six hours left. Pairing progress against the obligation with
// progress through the tracker's window is what turns this from a report of a
// breach into a warning before one.
//
// The window is measured from completion where the client knows it, since that
// is when most trackers start counting, falling back to when the torrent was
// added. Without a configured window the obligation is still evaluated, but no
// urgency can be inferred and the risk stays OK rather than pretending to know.
func EvaluateHnR(e TorrentHealthEntry, indexerName string, rules *HnRRules, windowDays float64, now time.Time) HnRStatus {
	s := HnRStatus{
		Hash:        e.Hash,
		Name:        e.Name,
		IndexerName: indexerName,
		Complete:    e.Progress >= 0.999,
		WindowDays:  windowDays,
		WindowKnown: windowDays > 0,
		Risk:        HnRRiskUnknown,
	}
	if rules == nil || (rules.MinSeedHours <= 0 && rules.MinRatio <= 0) {
		s.Detail = "no hit-and-run rules configured for this tracker"
		return s
	}
	s.RequiredHours = rules.MinSeedHours
	s.RequiredRatio = rules.MinRatio
	s.SeedingHours = e.SeedingHours
	s.Ratio = e.Ratio
	s.Satisfied = rules.Satisfied(e.SeedingHours, e.Ratio)

	if s.Satisfied {
		s.Risk = HnRRiskMet
		s.Detail = fmt.Sprintf("obligation met: seeded %.1fh, ratio %.2f", e.SeedingHours, e.Ratio)
		return s
	}

	// Obligation outstanding. Without a deadline there is no urgency to compute.
	if !s.WindowKnown {
		s.Risk = HnRRiskOK
		s.Detail = fmt.Sprintf("seeded %.1fh of %.0fh, ratio %.2f of %.2f; no tracker deadline configured",
			e.SeedingHours, rules.MinSeedHours, e.Ratio, rules.MinRatio)
		return s
	}

	start := e.CompletedAt
	if start.IsZero() || start.Unix() <= 0 {
		start = e.AddedAt
	}
	if start.IsZero() || start.Unix() <= 0 {
		s.Risk = HnRRiskOK
		s.Detail = "cannot tell when the obligation started"
		return s
	}

	deadline := start.Add(time.Duration(windowDays * float64(24*time.Hour)))
	s.HoursRemaining = deadline.Sub(now).Hours()
	elapsed := now.Sub(start).Hours()
	window := windowDays * 24
	used := 0.0
	if window > 0 {
		used = elapsed / window
	}

	switch {
	case s.HoursRemaining <= 0:
		s.Risk = HnRRiskCritical
		s.Detail = fmt.Sprintf("PAST the tracker deadline by %.1fh with the obligation unmet (seeded %.1fh of %.0fh, ratio %.2f of %.2f)",
			-s.HoursRemaining, e.SeedingHours, rules.MinSeedHours, e.Ratio, rules.MinRatio)
	case used >= 0.95:
		s.Risk = HnRRiskCritical
		s.Detail = fmt.Sprintf("only %.1fh left to meet the obligation (seeded %.1fh of %.0fh)",
			s.HoursRemaining, e.SeedingHours, rules.MinSeedHours)
	case used >= 0.8:
		s.Risk = HnRRiskWarning
		s.Detail = fmt.Sprintf("%.1fh left of the tracker's window, obligation not yet met (seeded %.1fh of %.0fh)",
			s.HoursRemaining, e.SeedingHours, rules.MinSeedHours)
	case used >= 0.5:
		s.Risk = HnRRiskWatch
		s.Detail = fmt.Sprintf("past half the window; seeded %.1fh of %.0fh with %.1fh remaining",
			e.SeedingHours, rules.MinSeedHours, s.HoursRemaining)
	default:
		s.Risk = HnRRiskOK
		s.Detail = fmt.Sprintf("seeded %.1fh of %.0fh, %.1fh of the window remaining",
			e.SeedingHours, rules.MinSeedHours, s.HoursRemaining)
	}
	return s
}
