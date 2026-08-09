package torrent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/indexer"
	"seedstream/pkg/services/cerberus"
)

// countingIndexer records how many replacement searches were attempted.
//
// Search is reachable from exactly one place in the watchdog — findReplacement,
// which sits behind the ReplaceStalled gate — so the call count is a precise
// probe for "did the watchdog try to start a second download of this title".
type countingIndexer struct{ searches int64 }

func (c *countingIndexer) Search(indexer.SearchRequest) (*indexer.SearchResponse, error) {
	atomic.AddInt64(&c.searches, 1)
	return nil, nil
}
func (c *countingIndexer) DownloadNZB(context.Context, string) ([]byte, error) { return nil, nil }
func (c *countingIndexer) Ping() error                                        { return nil }
func (c *countingIndexer) Name() string                                       { return "counting" }
func (c *countingIndexer) GetUsage() indexer.Usage                            { return indexer.Usage{} }

// newReplaceTestWatchdog builds a watchdog whose replacement search is
// observable. The qBittorrent URL is never dialled on this path: a zero-progress
// stall reports the failure and then either stops at the gate or goes to the
// indexer, so no client call happens before the assertion point.
func newReplaceTestWatchdog(t *testing.T, replaceStalled bool) (*Watchdog, *countingIndexer) {
	t.Helper()
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "seedbox", Type: "qbittorrent", URL: "http://127.0.0.1:1", Category: "seedstream",
	}})
	store, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	idx := &countingIndexer{}
	wd := NewWatchdog(mgr, cerberus.New(store), idx, &config.Config{}, nil)
	if wd == nil {
		t.Fatal("watchdog is nil")
	}
	wd.replaceStalled = replaceStalled
	return wd, idx
}

func zeroProgressStall() (TorrentHealthEntry, *cerberus.TorrentRecord) {
	stalled := TorrentHealthEntry{
		Hash:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:       "Some.Film.2026.2160p.REMUX",
		State:      "stalledDL",
		Progress:   0,
		ClientName: "seedbox",
		AddedAt:    time.Now().Add(-time.Hour),
	}
	// An ID must be present or findReplacement returns before ever searching,
	// which would make the enabled case pass for the wrong reason.
	rec := &cerberus.TorrentRecord{
		InfoHash:    stalled.Hash,
		ImdbID:      "tt1234567",
		IndexerName: "counting",
	}
	return stalled, rec
}

// TestZeroProgressStallDoesNotStartASecondDownload is the whole point of the
// gate: a download that never started must be left alone, not answered with a
// second copy of the same title. Replace() adds alongside and deletes nothing,
// so every replacement is permanent disk on the seedbox.
func TestZeroProgressStallDoesNotStartASecondDownload(t *testing.T) {
	wd, idx := newReplaceTestWatchdog(t, false)
	stalled, rec := zeroProgressStall()

	wd.handleStalled(context.Background(), stalled, rec)

	if got := atomic.LoadInt64(&idx.searches); got != 0 {
		t.Fatalf("replacement disabled, but the watchdog searched for one %d time(s)", got)
	}
}

// TestZeroProgressStallReplacesWhenExplicitlyEnabled proves the behaviour was
// gated rather than deleted: opting in still reaches the replacement search.
func TestZeroProgressStallReplacesWhenExplicitlyEnabled(t *testing.T) {
	wd, idx := newReplaceTestWatchdog(t, true)
	stalled, rec := zeroProgressStall()

	wd.handleStalled(context.Background(), stalled, rec)

	if got := atomic.LoadInt64(&idx.searches); got != 1 {
		t.Fatalf("replacement enabled, expected 1 search, got %d", got)
	}
}

// TestReplacementIsOffByDefault pins the default. main.go passes an empty
// WatchdogConfig, so a zero value that meant "replace" would silently restore
// the unbounded-duplicate behaviour.
func TestReplacementIsOffByDefault(t *testing.T) {
	if (WatchdogConfig{}).ReplaceStalled {
		t.Fatal("ReplaceStalled must default to false")
	}
}

// TestStartHonoursReplaceStalled covers the wiring: the flag has to survive the
// hop from WatchdogConfig into the struct field the check path reads, otherwise
// the option silently does nothing.
func TestStartHonoursReplaceStalled(t *testing.T) {
	wd, _ := newReplaceTestWatchdog(t, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Start returns at the first select; only the assignment runs.
	wd.Start(ctx, WatchdogConfig{ReplaceStalled: true})

	if !wd.replaceStalled {
		t.Fatal("Start did not carry ReplaceStalled onto the watchdog")
	}
}
