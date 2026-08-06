package torrent

import (
	"testing"
	"time"
)

func hnrEntry(seededHours, ratio float64, completedAgo time.Duration) TorrentHealthEntry {
	return TorrentHealthEntry{
		Hash: "h", Name: "Thing", State: "uploading", Progress: 1,
		SeedingHours: seededHours,
		Ratio:        ratio,
		CompletedAt:  time.Now().Add(-completedAgo),
		AddedAt:      time.Now().Add(-completedAgo),
	}
}

// strictRules requires both, so ratio cannot short-circuit the seed time.
func strictRules() *HnRRules { return &HnRRules{MinSeedHours: 72, MinRatio: 1.0, Mode: "all"} }

// TestHnRWarnsBeforeBreach is the whole point: the same seed time is fine early
// in the tracker's window and an emergency near the end. Seed time alone cannot
// tell those apart.
func TestHnRWarnsBeforeBreach(t *testing.T) {
	now := time.Now()
	const windowDays = 14

	early := EvaluateHnR(hnrEntry(40, 0.5, 24*time.Hour), "T", strictRules(), windowDays, now)
	if early.Risk != HnRRiskOK {
		t.Fatalf("40h seeded one day into a 14 day window should be OK, got %s (%s)", early.Risk, early.Detail)
	}

	// 13.5 of 14 days is 96% of the window gone with the obligation still unmet.
	late := EvaluateHnR(hnrEntry(40, 0.5, time.Duration(13.5*24*float64(time.Hour))), "T", strictRules(), windowDays, now)
	if late.Risk != HnRRiskCritical {
		t.Fatalf("the same 40h with half a day left should be critical, got %s (%s)", late.Risk, late.Detail)
	}
	if late.HoursRemaining <= 0 || late.HoursRemaining > 13 {
		t.Fatalf("hours remaining looks wrong: %.1f", late.HoursRemaining)
	}
}

// TestHnREscalatesThroughTheWindow checks the ladder rather than a single point.
func TestHnREscalatesThroughTheWindow(t *testing.T) {
	now := time.Now()
	const windowDays = 10
	const windowHours = windowDays * 24 // 240h
	cases := []struct {
		fractionUsed float64
		want         HnRRisk
	}{
		{0.10, HnRRiskOK},
		{0.60, HnRRiskWatch},
		{0.85, HnRRiskWarning},
		{0.97, HnRRiskCritical},
	}
	for _, c := range cases {
		elapsed := time.Duration(c.fractionUsed * windowHours * float64(time.Hour))
		got := EvaluateHnR(hnrEntry(10, 0.1, elapsed), "T", strictRules(), windowDays, now)
		if got.Risk != c.want {
			t.Errorf("%.0f%% of the window used: got %s want %s (%s)",
				c.fractionUsed*100, got.Risk, c.want, got.Detail)
		}
	}
}

// TestHnRPastDeadlineIsCritical: an unmet obligation past the deadline is the
// worst case and must be reported as such, not silently rolled into "warning".
func TestHnRPastDeadlineIsCritical(t *testing.T) {
	got := EvaluateHnR(hnrEntry(10, 0.1, 20*24*time.Hour), "T", strictRules(), 14, time.Now())
	if got.Risk != HnRRiskCritical {
		t.Fatalf("past the deadline must be critical, got %s", got.Risk)
	}
	if got.HoursRemaining >= 0 {
		t.Fatalf("hours remaining should be negative past the deadline, got %.1f", got.HoursRemaining)
	}
}

// TestHnRMetOutranksUrgency: once the obligation is discharged the deadline is
// irrelevant, even if the window has nearly elapsed.
func TestHnRMetOutranksUrgency(t *testing.T) {
	got := EvaluateHnR(hnrEntry(200, 3.0, 13*24*time.Hour), "T", strictRules(), 14, time.Now())
	if got.Risk != HnRRiskMet {
		t.Fatalf("a met obligation should not be urgent, got %s (%s)", got.Risk, got.Detail)
	}
	if !got.Satisfied {
		t.Fatal("should be marked satisfied")
	}
}

// TestHnRWithoutWindowClaimsNoUrgency: no deadline configured must read as
// "unknown urgency", never as a false all-clear or a false alarm.
func TestHnRWithoutWindowClaimsNoUrgency(t *testing.T) {
	got := EvaluateHnR(hnrEntry(10, 0.1, 30*24*time.Hour), "T", strictRules(), 0, time.Now())
	if got.Risk != HnRRiskOK {
		t.Fatalf("no window should stay OK rather than inventing urgency, got %s", got.Risk)
	}
	if got.WindowKnown {
		t.Fatal("WindowKnown must be false so the UI can distinguish it from a real all-clear")
	}
}

// TestHnRWithoutRulesIsUnknown keeps the fail-closed default consistent.
func TestHnRWithoutRulesIsUnknown(t *testing.T) {
	got := EvaluateHnR(hnrEntry(1, 0.1, time.Hour), "T", nil, 14, time.Now())
	if got.Risk != HnRRiskUnknown {
		t.Fatalf("no rules must read as unknown, got %s", got.Risk)
	}
	if got.Urgent() {
		t.Fatal("unknown must not be urgent")
	}
}
