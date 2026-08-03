package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"seedstream/pkg/core/config"
)

// newStatsQBit serves /torrents/info with the given per-torrent speeds and swarm
// counts, counting how many times it was polled. Set fail to make every call
// error, standing in for an unreachable seedbox.
func newStatsQBit(t *testing.T, dl, up int64, seeds, leechs int, fail bool) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "t"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"hash":"%040d","name":"t","size":1,"progress":1,"state":"uploading","dlspeed":%d,"upspeed":%d,"num_seeds":%d,"num_leechs":%d,"downloaded":10,"uploaded":20}]`,
			1, dl, up, seeds, leechs)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func managerFor(t *testing.T, urls map[string]string) *Manager {
	t.Helper()
	var cfgs []config.TorrentClientConfig
	for name, u := range urls {
		cfgs = append(cfgs, config.TorrentClientConfig{
			Name: name, Type: "qbittorrent", URL: u, Category: "seedstream",
		})
	}
	return NewManager(cfgs)
}

// TestAggregateStatsPerClientBreakdown is the regression guard for the bug where
// the dashboard rendered fleet-wide seed/peer totals on every client's card. Each
// client must report its own figures.
func TestAggregateStatsPerClientBreakdown(t *testing.T) {
	a, _ := newStatsQBit(t, 1_000_000, 500_000, 10, 2, false)
	b, _ := newStatsQBit(t, 3_000_000, 250_000, 7, 5, false)

	m := managerFor(t, map[string]string{"alpha": a.URL, "beta": b.URL})
	got := m.AggregateStats(context.Background())

	if len(got.Clients) != 2 {
		t.Fatalf("expected 2 per-client entries, got %d", len(got.Clients))
	}
	byName := map[string]ClientStats{}
	for _, c := range got.Clients {
		byName[c.Name] = c
	}

	if byName["alpha"].Seeds != 10 || byName["alpha"].Peers != 2 {
		t.Fatalf("alpha = %+v, want seeds 10 / peers 2", byName["alpha"])
	}
	if byName["beta"].Seeds != 7 || byName["beta"].Peers != 5 {
		t.Fatalf("beta = %+v, want seeds 7 / peers 5", byName["beta"])
	}
	// The two clients must not report identical figures — that was the bug.
	if byName["alpha"].Seeds == byName["beta"].Seeds {
		t.Fatalf("clients reported identical seed counts; per-client breakdown is not being applied")
	}
	// Totals must still be the sum.
	if got.Seeds != 17 || got.Peers != 7 {
		t.Fatalf("fleet totals = seeds %d / peers %d, want 17 / 7", got.Seeds, got.Peers)
	}
	if got.DownloadSpeedBps != 4_000_000 {
		t.Fatalf("fleet download = %d, want 4000000", got.DownloadSpeedBps)
	}
}

// TestAggregateStatsMarksUnreachableClient checks a failing client is reported
// as unreachable rather than as an idle one showing zeros.
func TestAggregateStatsMarksUnreachableClient(t *testing.T) {
	ok, _ := newStatsQBit(t, 1_000_000, 0, 4, 1, false)
	bad, _ := newStatsQBit(t, 0, 0, 0, 0, true)

	m := managerFor(t, map[string]string{"good": ok.URL, "down": bad.URL})
	got := m.AggregateStats(context.Background())

	byName := map[string]ClientStats{}
	for _, c := range got.Clients {
		byName[c.Name] = c
	}
	if !byName["good"].Reachable {
		t.Fatalf("healthy client marked unreachable: %+v", byName["good"])
	}
	if byName["down"].Reachable {
		t.Fatalf("failing client should be marked unreachable: %+v", byName["down"])
	}
	// One client failing must not lose the other's numbers.
	if got.Seeds != 4 {
		t.Fatalf("fleet seeds = %d, want 4 from the reachable client", got.Seeds)
	}
}

// TestAggregateStatsSingleFlight checks concurrent callers collapse onto one
// refresh instead of each hitting the seedbox, which is the load this cache
// exists to prevent.
func TestAggregateStatsSingleFlight(t *testing.T) {
	srv, calls := newStatsQBit(t, 1, 1, 1, 1, false)
	m := managerFor(t, map[string]string{"only": srv.URL})

	const callers = 25
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			m.AggregateStats(context.Background())
		}()
	}
	wg.Wait()

	if n := atomic.LoadInt32(calls); n != 1 {
		t.Fatalf("%d concurrent callers produced %d qBittorrent polls, want exactly 1", callers, n)
	}
}
