package cardigann

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"seedstream/pkg/indexer"
)

// newUsageManager gives each test its own counters, isolated from the
// process-wide instance the application uses.
func newUsageManager(t *testing.T) *indexer.UsageManager {
	t.Helper()
	um, err := indexer.NewUsageManager(nil)
	if err != nil {
		t.Fatalf("NewUsageManager: %v", err)
	}
	return um
}

// statsClient builds a Client over the same fake tracker the engine tests use,
// so the counters are exercised against a real login-and-scrape rather than a
// stub.
func statsClient(t *testing.T, f *fakeTracker) *Client {
	t.Helper()
	srv := f.server(t)
	def, err := Parse([]byte(fmt.Sprintf(testDefinition, srv.URL)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	cat := NewCatalog("")
	cat.mu.Lock()
	cat.defs[def.ID] = def
	cat.mu.Unlock()

	c, err := NewClient(cat, def.ID, "Fake Tracker", srv.URL,
		map[string]string{"username": "alice", "password": "hunter2"}, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// TestUsageIsMeasuredNotZero is Bug 1. GetUsage returned an empty struct, so a
// definition-driven tracker reported zero for everything — indistinguishable on
// the statistics page from one that had never run at all.
func TestUsageIsMeasuredNotZero(t *testing.T) {
	c := statsClient(t, &fakeTracker{})

	if u := c.GetUsage(); u.SearchesCount != 0 || u.APIHitsUsed != 0 {
		t.Fatalf("a tracker that has done nothing should report nothing, got %+v", u)
	}

	if _, err := c.Search(indexer.SearchRequest{Query: "some movie"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	u := c.GetUsage()
	if u.SearchesCount != 1 {
		t.Errorf("searches count = %d, want 1", u.SearchesCount)
	}
	if u.APIHitsUsed != 1 {
		t.Errorf("api hits used = %d, want 1 — a scrape is a request against the tracker", u.APIHitsUsed)
	}
	if u.AvgResponseMS < 0 {
		t.Errorf("average response time must not be negative, got %v", u.AvgResponseMS)
	}
}

// TestFailedSearchStillCountsAsARequest: a search that errored still hit the
// tracker. Counting only successes would make a broken tracker look quiet
// rather than broken, which is the opposite of what the page is for.
func TestFailedSearchStillCountsAsARequest(t *testing.T) {
	// Wrong credentials: login fails, so the search does too.
	f := &fakeTracker{}
	srv := f.server(t)
	def, err := Parse([]byte(fmt.Sprintf(testDefinition, srv.URL)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	cat := NewCatalog("")
	cat.mu.Lock()
	cat.defs[def.ID] = def
	cat.mu.Unlock()
	c, err := NewClient(cat, def.ID, "Fake Tracker", srv.URL,
		map[string]string{"username": "wrong", "password": "wrong"}, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := c.Search(indexer.SearchRequest{Query: "x"}); err == nil {
		t.Fatal("precondition: bad credentials should fail the search")
	}
	u := c.GetUsage()
	if u.APIHitsUsed != 1 || u.SearchesCount != 1 {
		t.Fatalf("a failed search is still a request, got hits=%d searches=%d",
			u.APIHitsUsed, u.SearchesCount)
	}
}

// TestUsageCountersAreRaceFree: the statistics page reads these while searches
// write them, so every access has to be guarded.
func TestUsageCountersAreRaceFree(t *testing.T) {
	c := statsClient(t, &fakeTracker{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Search(indexer.SearchRequest{Query: "concurrent"})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.GetUsage()
		}()
	}
	wg.Wait()

	if got := c.GetUsage().SearchesCount; got != 8 {
		t.Fatalf("every search must be counted exactly once, got %d of 8", got)
	}
}

// TestRemainingIsOnlyReportedWhenALimitExists: a scraped tracker publishes no
// quota, so reporting a remaining figure against a limit of zero would invent a
// budget the tracker never gave.
func TestRemainingIsOnlyReportedWhenALimitExists(t *testing.T) {
	c := statsClient(t, &fakeTracker{})
	if _, err := c.Search(indexer.SearchRequest{Query: "x"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	if u := c.GetUsage(); u.APIHitsLimit != 0 || u.APIHitsRemaining != 0 {
		t.Fatalf("with no configured limit both must stay zero, got limit=%d remaining=%d",
			u.APIHitsLimit, u.APIHitsRemaining)
	}

	c.mu.Lock()
	c.apiLimit = 2
	c.mu.Unlock()
	if u := c.GetUsage(); u.APIHitsRemaining != 1 {
		t.Errorf("remaining = %d, want 1", u.APIHitsRemaining)
	}

	// Over budget must clamp at zero rather than going negative.
	c.mu.Lock()
	c.apiUsed = 5
	c.mu.Unlock()
	if u := c.GetUsage(); u.APIHitsRemaining != 0 {
		t.Errorf("remaining must clamp at zero, got %d", u.APIHitsRemaining)
	}
}

// TestSearchStillBehavesTheSame guards the refactor behind Bug 2's fix: Search
// was split into a recording wrapper and the original body, and everything
// observable about it must be unchanged.
func TestSearchStillBehavesTheSame(t *testing.T) {
	f := &fakeTracker{}
	c := statsClient(t, f)

	resp, err := c.Search(indexer.SearchRequest{Query: "some movie"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp == nil || len(resp.Channel.Items) == 0 {
		t.Fatal("expected at least one result")
	}
	if !strings.Contains(resp.Channel.Items[0].Title, "Some.Movie") {
		t.Fatalf("unexpected title %q", resp.Channel.Items[0].Title)
	}
	if f.query() != "some movie" {
		t.Errorf("the query must still reach the tracker, got %q", f.query())
	}

	// The limit truncation the original method applied must still apply.
	limited, err := c.Search(indexer.SearchRequest{Query: "some movie", Limit: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(limited.Channel.Items) > 1 {
		t.Fatalf("limit not honoured, got %d items", len(limited.Channel.Items))
	}
}

// TestGrabsRecordedElsewhereSurfaceHere closes the loop between Bug 4's fix and
// Bug 1's. Grabs are counted at playback, against the shared usage manager
// rather than against this client, so GetUsage has to read them back — without
// that the download column stays at zero however much was actually grabbed,
// which is the same symptom the bug report described.
func TestGrabsRecordedElsewhereSurfaceHere(t *testing.T) {
	um := newUsageManager(t)
	f := &fakeTracker{}
	srv := f.server(t)
	def, err := Parse([]byte(fmt.Sprintf(testDefinition, srv.URL)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	cat := NewCatalog("")
	cat.mu.Lock()
	cat.defs[def.ID] = def
	cat.mu.Unlock()

	c, err := NewClient(cat, def.ID, "Fake Tracker", srv.URL,
		map[string]string{"username": "alice", "password": "hunter2"}, 10*time.Second, um)
	if err != nil {
		t.Fatalf("NewClientWithUsage: %v", err)
	}

	if u := c.GetUsage(); u.DownloadsUsed != 0 {
		t.Fatalf("nothing grabbed yet, got %d", u.DownloadsUsed)
	}

	// What the playback path does when a torrent reaches a download client.
	um.IncrementUsed("Fake Tracker", 0, 1)
	um.IncrementUsed("Fake Tracker", 0, 1)

	u := c.GetUsage()
	if u.DownloadsUsed != 2 {
		t.Errorf("downloads used = %d, want 2", u.DownloadsUsed)
	}
	if u.AllTimeDownloadsUsed != 2 {
		t.Errorf("all-time downloads = %d, want 2", u.AllTimeDownloadsUsed)
	}
}

// TestSearchesPersistToTheUsageManager: counters that live only in memory reset
// to zero on every restart, which is indistinguishable from a tracker that has
// never run.
func TestSearchesPersistToTheUsageManager(t *testing.T) {
	um := newUsageManager(t)
	f := &fakeTracker{}
	srv := f.server(t)
	def, err := Parse([]byte(fmt.Sprintf(testDefinition, srv.URL)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	cat := NewCatalog("")
	cat.mu.Lock()
	cat.defs[def.ID] = def
	cat.mu.Unlock()
	settings := map[string]string{"username": "alice", "password": "hunter2"}

	c, err := NewClient(cat, def.ID, "Fake Tracker", srv.URL, settings, 10*time.Second, um)
	if err != nil {
		t.Fatalf("NewClientWithUsage: %v", err)
	}
	if _, err := c.Search(indexer.SearchRequest{Query: "some movie"}); err != nil {
		t.Fatalf("search: %v", err)
	}

	// A fresh client over the same usage manager is what a restart looks like.
	restarted, err := NewClient(cat, def.ID, "Fake Tracker", srv.URL, settings, 10*time.Second, um)
	if err != nil {
		t.Fatalf("NewClientWithUsage: %v", err)
	}
	if got := restarted.GetUsage().APIHitsUsed; got != 1 {
		t.Fatalf("today's request count must survive a restart, got %d", got)
	}
}
