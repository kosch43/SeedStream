package persistence

import (
	"math"
	"testing"
	"time"
)

func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 0.001
}

func findIndexerRow(rows []IndexerStatsRow, name string) (IndexerStatsRow, bool) {
	for _, r := range rows {
		if r.IndexerName == name {
			return r, true
		}
	}
	return IndexerStatsRow{}, false
}

func findProviderRow(rows []ProviderStatsRow, name string) (ProviderStatsRow, bool) {
	for _, r := range rows {
		if r.ProviderName == name {
			return r, true
		}
	}
	return ProviderStatsRow{}, false
}

func TestGetIndexerStatsAggregation(t *testing.T) {
	mgr := newTestStateManager(t)
	now := time.Now()
	ts := now.Add(-1 * time.Hour)

	// Indexer A: 3 searches, 2 successful (response 100, 300 -> avg 200; results 4, 6 -> avg 5),
	// 1 failed. success_rate = 2/3 = 66.666...%
	mustRecordSearch(t, mgr, ts, "A", true, 100, 4)
	mustRecordSearch(t, mgr, ts, "A", true, 300, 6)
	mustRecordSearch(t, mgr, ts, "A", false, 999, 0)

	// Indexer B: 1 successful search.
	mustRecordSearch(t, mgr, ts, "B", true, 50, 10)

	// Total searches = 4. A share = 3/4 = 75%, B share = 1/4 = 25%.

	// Downloads.
	// A: two successful downloads. One is unique (indexers_with_result=1) with
	// 10 indexers in search -> uniqueness 1000. Other: 4 in search / 2 with result -> 200.
	// avg uniqueness for A = (1000 + 200) / 2 = 600. unique_downloads = 1.
	mustRecordDownload(t, mgr, ts, "A", true, 10, 1)
	mustRecordDownload(t, mgr, ts, "A", true, 4, 2)
	// A: one failed download (should not count toward downloads, but uniqueness avg
	// is over all download events: 5 in search / 5 with result -> 100).
	mustRecordDownload(t, mgr, ts, "A", false, 5, 5)
	// B: one successful, unique download. 2 in search / 1 with result -> 200.
	mustRecordDownload(t, mgr, ts, "B", true, 2, 1)

	// Total downloads (successful) = 2 (A) + 1 (B) = 3.

	from := now.Add(-2 * time.Hour)
	to := now
	rows, err := mgr.GetIndexerStats(&from, &to)
	if err != nil {
		t.Fatalf("GetIndexerStats: %v", err)
	}

	a, ok := findIndexerRow(rows, "A")
	if !ok {
		t.Fatal("indexer A missing")
	}
	if a.Searches != 3 {
		t.Errorf("A searches = %d, want 3", a.Searches)
	}
	if !approxEq(a.SearchSharePct, 75.0) {
		t.Errorf("A search_share = %f, want 75", a.SearchSharePct)
	}
	if !approxEq(a.AvgResponseMS, 200.0) {
		t.Errorf("A avg_response = %f, want 200", a.AvgResponseMS)
	}
	if !approxEq(a.AvgResults, 5.0) {
		t.Errorf("A avg_results = %f, want 5", a.AvgResults)
	}
	if !approxEq(a.SuccessRatePct, 200.0/3.0) {
		t.Errorf("A success_rate = %f, want 66.666", a.SuccessRatePct)
	}
	if a.Downloads != 2 {
		t.Errorf("A downloads = %d, want 2", a.Downloads)
	}
	if !approxEq(a.DownloadSharePct, 100.0*2.0/3.0) {
		t.Errorf("A download_share = %f, want 66.666", a.DownloadSharePct)
	}
	if a.UniqueDownloads != 1 {
		t.Errorf("A unique_downloads = %d, want 1", a.UniqueDownloads)
	}
	// avg over all 3 download events: (1000 + 200 + 100)/3 = 433.333
	if !approxEq(a.AvgUniquenessScore, (1000.0+200.0+100.0)/3.0) {
		t.Errorf("A avg_uniqueness = %f, want 433.333", a.AvgUniquenessScore)
	}

	b, ok := findIndexerRow(rows, "B")
	if !ok {
		t.Fatal("indexer B missing")
	}
	if !approxEq(b.SearchSharePct, 25.0) {
		t.Errorf("B search_share = %f, want 25", b.SearchSharePct)
	}
	if !approxEq(b.SuccessRatePct, 100.0) {
		t.Errorf("B success_rate = %f, want 100", b.SuccessRatePct)
	}
	if b.UniqueDownloads != 1 {
		t.Errorf("B unique_downloads = %d, want 1", b.UniqueDownloads)
	}
	if !approxEq(b.AvgUniquenessScore, 200.0) {
		t.Errorf("B avg_uniqueness = %f, want 200", b.AvgUniquenessScore)
	}
}

func TestGetIndexerStatsWindowExcludesOutOfRange(t *testing.T) {
	mgr := newTestStateManager(t)
	now := time.Now()
	mustRecordSearch(t, mgr, now.Add(-10*time.Hour), "A", true, 100, 1) // out of window
	mustRecordSearch(t, mgr, now.Add(-1*time.Hour), "A", true, 100, 1)  // in window

	from := now.Add(-2 * time.Hour)
	to := now
	rows, err := mgr.GetIndexerStats(&from, &to)
	if err != nil {
		t.Fatalf("GetIndexerStats: %v", err)
	}
	a, ok := findIndexerRow(rows, "A")
	if !ok {
		t.Fatal("indexer A missing")
	}
	if a.Searches != 1 {
		t.Errorf("windowed searches = %d, want 1", a.Searches)
	}
}

func TestGetProviderStatsAggregation(t *testing.T) {
	mgr := newTestStateManager(t)
	now := time.Now()
	ts := now.Add(-1 * time.Hour)

	// Provider P1: deltas summing to available=80, missing=20 (success 80%),
	// bytes = 1000.
	mustRecordProvider(t, mgr, ts, "P1", "h1", 50, 10, 600)
	mustRecordProvider(t, mgr, ts, "P1", "h1", 30, 10, 400)
	// Provider P2: available=10, missing=0, bytes=3000.
	mustRecordProvider(t, mgr, ts, "P2", "h2", 10, 0, 3000)

	// Total bytes = 4000. P1 share = 1000/4000 = 25%, P2 = 75%.

	from := now.Add(-2 * time.Hour)
	to := now
	rows, err := mgr.GetProviderStats(&from, &to)
	if err != nil {
		t.Fatalf("GetProviderStats: %v", err)
	}

	p1, ok := findProviderRow(rows, "P1")
	if !ok {
		t.Fatal("provider P1 missing")
	}
	if p1.Available != 80 || p1.Missing != 20 {
		t.Errorf("P1 available/missing = %d/%d, want 80/20", p1.Available, p1.Missing)
	}
	if p1.Articles != 100 {
		t.Errorf("P1 articles = %d, want 100", p1.Articles)
	}
	if !approxEq(p1.SuccessRatePct, 80.0) {
		t.Errorf("P1 success_rate = %f, want 80", p1.SuccessRatePct)
	}
	if p1.DownloadedBytes != 1000 {
		t.Errorf("P1 bytes = %d, want 1000", p1.DownloadedBytes)
	}
	if !approxEq(p1.DataSharePct, 25.0) {
		t.Errorf("P1 data_share = %f, want 25", p1.DataSharePct)
	}
	if p1.Host != "h1" {
		t.Errorf("P1 host = %q, want h1", p1.Host)
	}

	p2, ok := findProviderRow(rows, "P2")
	if !ok {
		t.Fatal("provider P2 missing")
	}
	if !approxEq(p2.SuccessRatePct, 100.0) {
		t.Errorf("P2 success_rate = %f, want 100", p2.SuccessRatePct)
	}
	if !approxEq(p2.DataSharePct, 75.0) {
		t.Errorf("P2 data_share = %f, want 75", p2.DataSharePct)
	}
}

func mustRecordSearch(t *testing.T, mgr *StateManager, ts time.Time, name string, success bool, responseMS int64, resultCount int) {
	t.Helper()
	if err := mgr.RecordIndexerSearchEvent(ts, name, success, responseMS, resultCount); err != nil {
		t.Fatalf("RecordIndexerSearchEvent: %v", err)
	}
}

func mustRecordDownload(t *testing.T, mgr *StateManager, ts time.Time, name string, success bool, inSearch, withResult int) {
	t.Helper()
	if err := mgr.RecordIndexerDownloadEvent(ts, name, success, inSearch, withResult); err != nil {
		t.Fatalf("RecordIndexerDownloadEvent: %v", err)
	}
}

func mustRecordProvider(t *testing.T, mgr *StateManager, ts time.Time, name, host string, avail, missing, bytes int64) {
	t.Helper()
	if err := mgr.RecordProviderAccessDelta(ts, name, host, avail, missing, bytes); err != nil {
		t.Fatalf("RecordProviderAccessDelta: %v", err)
	}
}
