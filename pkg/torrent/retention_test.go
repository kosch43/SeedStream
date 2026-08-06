package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"seedstream/pkg/core/config"
)

// retentionQBit serves a single completed torrent with configurable seeding
// figures and tracker state.
type retentionQBit struct {
	seedingSeconds int64
	ratio          float64
	trackerStatus  int
	trackersFail   bool
}

const retentionHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (q *retentionQBit) manager(t *testing.T) *Manager {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%s","name":"Thing","size":100,"progress":1,"state":"uploading","seeding_time":%d,"ratio":%v}]`,
			retentionHash, q.seedingSeconds, q.ratio)
	})
	mux.HandleFunc("/api/v2/torrents/trackers", func(w http.ResponseWriter, r *http.Request) {
		if q.trackersFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `[{"url":"** [DHT] **","status":0,"msg":""},{"url":"https://tracker/announce","status":%d,"msg":""}]`, q.trackerStatus)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: srv.URL, Category: "seedstream",
	}})
}

func completedEntry() TorrentHealthEntry {
	return TorrentHealthEntry{
		ClientName: "box", Hash: retentionHash, Name: "Thing",
		State: "uploading", Progress: 1,
	}
}

func rules72h() *HnRRules { return &HnRRules{MinSeedHours: 72, MinRatio: 1.0, Mode: "any"} }

// TestRetentionEligibleWhenProvablyMet: the one case that may pass — complete,
// opted in, tracker answering, and past both thresholds including the margin.
func TestRetentionEligibleWhenProvablyMet(t *testing.T) {
	q := &retentionQBit{seedingSeconds: 200 * 3600, ratio: 2.0, trackerStatus: 2}
	v := q.manager(t).EvaluateRetention(context.Background(), completedEntry(), "Tracker", rules72h(), true, 50)

	if !v.Eligible {
		t.Fatalf("should be eligible: %s", v.Reason)
	}
	if v.RequiredHours != 108 { // 72h + 50% margin
		t.Fatalf("required hours should include the margin, got %.1f", v.RequiredHours)
	}
}

// TestRetentionRefusesWithoutRules is the most important guard. HnRRules.Satisfied
// treats nil as satisfied because it is a warning helper; if retention inherited
// that, every torrent from an unconfigured tracker would look instantly
// disposable.
func TestRetentionRefusesWithoutRules(t *testing.T) {
	q := &retentionQBit{seedingSeconds: 10000 * 3600, ratio: 99, trackerStatus: 2}
	m := q.manager(t)

	if v := m.EvaluateRetention(context.Background(), completedEntry(), "Tracker", nil, true, 50); v.Eligible {
		t.Fatal("no configured rules must never be read as obligations met")
	}
	empty := &HnRRules{}
	if v := m.EvaluateRetention(context.Background(), completedEntry(), "Tracker", empty, true, 50); v.Eligible {
		t.Fatal("empty rules must never be read as obligations met")
	}
	// Sanity: the warning helper really does default the other way.
	if !(*HnRRules)(nil).Satisfied(0, 0) {
		t.Fatal("precondition: Satisfied treats nil as satisfied, which is why retention must not use it")
	}
}

// TestRetentionRequiresOptIn: a tracker the operator has not reviewed is never
// considered, no matter how healthy it looks.
func TestRetentionRequiresOptIn(t *testing.T) {
	q := &retentionQBit{seedingSeconds: 10000 * 3600, ratio: 99, trackerStatus: 2}
	if v := q.manager(t).EvaluateRetention(context.Background(), completedEntry(), "Tracker", rules72h(), false, 50); v.Eligible {
		t.Fatal("a tracker that was not opted in must never be eligible")
	}
}

// TestRetentionRefusesWhenTrackerSilent is the false-confidence guard: seeding
// time and ratio are counted locally, so a tracker that is not answering may
// have recorded far less than qBittorrent shows.
func TestRetentionRefusesWhenTrackerSilent(t *testing.T) {
	for _, status := range []int{1, 4} { // not contacted, not working
		q := &retentionQBit{seedingSeconds: 10000 * 3600, ratio: 99, trackerStatus: status}
		v := q.manager(t).EvaluateRetention(context.Background(), completedEntry(), "Tracker", rules72h(), true, 50)
		if v.Eligible {
			t.Fatalf("tracker status %d must block eligibility", status)
		}
	}
	// And an unreadable tracker list is equally disqualifying.
	q := &retentionQBit{seedingSeconds: 10000 * 3600, ratio: 99, trackersFail: true}
	if v := q.manager(t).EvaluateRetention(context.Background(), completedEntry(), "Tracker", rules72h(), true, 50); v.Eligible {
		t.Fatal("an unreadable tracker status must block eligibility")
	}
}

// TestRetentionRequiresBothThresholds: the any/all mode is a warning nuance.
// For removal, everything that is set must be satisfied.
func TestRetentionRequiresBothThresholds(t *testing.T) {
	// Ratio satisfied, seed time far short — "any" mode would call this clear.
	q := &retentionQBit{seedingSeconds: 2 * 3600, ratio: 5.0, trackerStatus: 2}
	r := rules72h()
	if r.Satisfied(2, 5.0) != true {
		t.Fatal("precondition: any-mode warning logic considers this satisfied")
	}
	if v := q.manager(t).EvaluateRetention(context.Background(), completedEntry(), "Tracker", r, true, 50); v.Eligible {
		t.Fatal("retention must require both thresholds, not either")
	}
}

// TestRetentionAppliesSafetyMargin: meeting the bare requirement is not enough.
func TestRetentionAppliesSafetyMargin(t *testing.T) {
	q := &retentionQBit{seedingSeconds: 80 * 3600, ratio: 2.0, trackerStatus: 2} // past 72h, under 108h
	if v := q.manager(t).EvaluateRetention(context.Background(), completedEntry(), "Tracker", rules72h(), true, 50); v.Eligible {
		t.Fatal("80h must not clear a 72h requirement carrying a 50% margin")
	}
}

// TestRetentionRefusesIncomplete: a partial download owes seeding it cannot
// have performed.
func TestRetentionRefusesIncomplete(t *testing.T) {
	q := &retentionQBit{seedingSeconds: 10000 * 3600, ratio: 99, trackerStatus: 2}
	e := completedEntry()
	e.Progress = 0.5
	if v := q.manager(t).EvaluateRetention(context.Background(), e, "Tracker", rules72h(), true, 50); v.Eligible {
		t.Fatal("an incomplete torrent must never be eligible")
	}
}

// TestSafetyMarginCannotBeLowered: the configured margin is a floor.
func TestSafetyMarginCannotBeLowered(t *testing.T) {
	for _, in := range []int{-100, 0, 10, 49} {
		cfg := &config.Config{HnRSafetyMarginPercent: in}
		if got := cfg.EffectiveHnRSafetyMarginPercent(); got != config.DefaultHnRSafetyMarginPercent {
			t.Errorf("margin %d should be raised to the default, got %d", in, got)
		}
	}
	cfg := &config.Config{HnRSafetyMarginPercent: 200}
	if got := cfg.EffectiveHnRSafetyMarginPercent(); got != 200 {
		t.Fatalf("a larger margin should be honoured, got %d", got)
	}
}
