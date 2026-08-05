package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/indexer"
	"seedstream/pkg/services/cerberus"
)

func TestIsPausedState(t *testing.T) {
	paused := []string{"pausedUP", "pausedDL", "stoppedUP", "stoppedDL"}
	for _, s := range paused {
		if !isPausedState(s) {
			t.Errorf("%q should be paused", s)
		}
	}
	notPaused := []string{"uploading", "downloading", "stalledUP", "stalledDL", "metaDL", "queuedUP", "checkingUP", "error"}
	for _, s := range notPaused {
		if isPausedState(s) {
			t.Errorf("%q should not be paused", s)
		}
	}
}

func TestIsStalledMetaDL(t *testing.T) {
	threshold := 10 * time.Minute
	// metaDL added long ago with no activity: hung fetching metadata -> stalled.
	old := TorrentHealthEntry{State: "metaDL", Progress: 0, AddedAt: time.Now().Add(-time.Hour)}
	if !isStalled(old, threshold) {
		t.Fatalf("old metaDL should be stalled")
	}
	// Freshly added metaDL: not yet stalled.
	fresh := TorrentHealthEntry{State: "metaDL", Progress: 0, AddedAt: time.Now()}
	if isStalled(fresh, threshold) {
		t.Fatalf("fresh metaDL should not be stalled yet")
	}
}

// resumeTrackingQBit records which torrents got a resume/start call.
type resumeTrackingQBit struct {
	state       string
	progress    float64
	resumeCalls int64
	startCalls  int64
}

func (q *resumeTrackingQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%040d","name":"Paused Movie","size":1000,"progress":%v,"state":%q,"category":"seedstream","added_on":1,"last_activity":2}]`,
			1, q.progress, q.state)
	})
	mux.HandleFunc("/api/v2/torrents/start", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.startCalls, 1)
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/resume", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.resumeCalls, 1)
		fmt.Fprint(w, "Ok.")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// stubIndexer satisfies indexer.Indexer for watchdog construction.
type stubIndexer struct{}

func (stubIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) { return nil, nil }
func (stubIndexer) DownloadNZB(context.Context, string) ([]byte, error)           { return nil, nil }
func (stubIndexer) Ping() error                                                   { return nil }
func (stubIndexer) Name() string                                                  { return "stub" }
func (stubIndexer) GetUsage() indexer.Usage                                       { return indexer.Usage{} }

func newTestWatchdog(t *testing.T, qbitURL string) *Watchdog {
	t.Helper()
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "seedbox", Type: "qbittorrent", URL: qbitURL, Category: "seedstream",
	}})
	store, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	cer := cerberus.New(store)
	wd := NewWatchdog(mgr, cer, stubIndexer{}, &config.Config{})
	if wd == nil {
		t.Fatal("watchdog is nil")
	}
	return wd
}

// TestWatchdogResumesPausedCompletedTorrent is the "not seeding" fix: a
// completed torrent that is paused must be resumed so it keeps seeding.
func TestWatchdogResumesPausedCompletedTorrent(t *testing.T) {
	q := &resumeTrackingQBit{state: "pausedUP", progress: 1.0}
	wd := newTestWatchdog(t, q.server(t).URL)

	wd.check(context.Background(), 10*time.Minute)

	if got := atomic.LoadInt64(&q.startCalls); got != 1 {
		t.Fatalf("expected 1 start (resume) call for paused completed torrent, got %d", got)
	}
}

// TestWatchdogResumesStoppedTorrent covers the qBittorrent 5.0 "stopped"
// spelling.
func TestWatchdogResumesStoppedTorrent(t *testing.T) {
	q := &resumeTrackingQBit{state: "stoppedUP", progress: 1.0}
	wd := newTestWatchdog(t, q.server(t).URL)

	wd.check(context.Background(), 10*time.Minute)

	if got := atomic.LoadInt64(&q.startCalls); got != 1 {
		t.Fatalf("expected 1 start call for stopped torrent, got %d", got)
	}
}

// TestWatchdogLeavesSeedingTorrentAlone: a normally-seeding torrent must not be
// touched.
func TestWatchdogLeavesSeedingTorrentAlone(t *testing.T) {
	q := &resumeTrackingQBit{state: "uploading", progress: 1.0}
	wd := newTestWatchdog(t, q.server(t).URL)

	wd.check(context.Background(), 10*time.Minute)

	if got := atomic.LoadInt64(&q.startCalls) + atomic.LoadInt64(&q.resumeCalls); got != 0 {
		t.Fatalf("a seeding torrent must not be resumed, got %d calls", got)
	}
}
