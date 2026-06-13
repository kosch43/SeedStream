package indexer

import (
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

var (
	usageManagerTestStateOnce sync.Once
	usageManagerTestStateMgr  *persistence.StateManager
	usageManagerTestStateErr  error
)

func testUsageManagerState(t *testing.T) *persistence.StateManager {
	t.Helper()

	usageManagerTestStateOnce.Do(func() {
		tempDir, err := os.MkdirTemp("", "seedstream-indexer-usage-")
		if err != nil {
			usageManagerTestStateErr = err
			return
		}
		usageManagerTestStateMgr, usageManagerTestStateErr = persistence.GetManager(tempDir)
	})
	if usageManagerTestStateErr != nil {
		t.Fatalf("GetManager: %v", usageManagerTestStateErr)
	}
	return usageManagerTestStateMgr
}

func newTestUsageManager(t *testing.T) *UsageManager {
	t.Helper()
	return &UsageManager{
		state: testUsageManagerState(t),
		data:  make(map[string]*UsageData),
	}
}

func TestGetIndexerUsageResetsDailyCountersWithoutChangingAllTime(t *testing.T) {
	um := newTestUsageManager(t)
	name := "usage-reset-get"
	um.data[name] = &UsageData{
		LastResetDay:         time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
		APIHitsUsed:          7,
		DownloadsUsed:        3,
		AllTimeAPIHitsUsed:   20,
		AllTimeDownloadsUsed: 11,
	}

	got := um.GetIndexerUsage(name)

	if got.APIHitsUsed != 0 || got.DownloadsUsed != 0 {
		t.Fatalf("expected daily usage to reset, got hits=%d downloads=%d", got.APIHitsUsed, got.DownloadsUsed)
	}
	if got.AllTimeAPIHitsUsed != 20 || got.AllTimeDownloadsUsed != 11 {
		t.Fatalf("expected all-time usage unchanged, got hits=%d downloads=%d", got.AllTimeAPIHitsUsed, got.AllTimeDownloadsUsed)
	}
}

func TestUpdateUsageOnNewDayOnlyAddsNewUsageToAllTime(t *testing.T) {
	um := newTestUsageManager(t)
	name := "usage-reset-update"
	um.data[name] = &UsageData{
		LastResetDay:         time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
		APIHitsUsed:          7,
		DownloadsUsed:        3,
		AllTimeAPIHitsUsed:   20,
		AllTimeDownloadsUsed: 11,
	}

	um.UpdateUsage(name, 2, 1)
	got := um.GetIndexerUsage(name)
	if got.APIHitsUsed != 2 || got.DownloadsUsed != 1 {
		t.Fatalf("expected new daily usage to be stored, got hits=%d downloads=%d", got.APIHitsUsed, got.DownloadsUsed)
	}
	if got.AllTimeAPIHitsUsed != 22 || got.AllTimeDownloadsUsed != 12 {
		t.Fatalf("expected all-time usage to increase only by new usage, got hits=%d downloads=%d", got.AllTimeAPIHitsUsed, got.AllTimeDownloadsUsed)
	}

	um.UpdateUsage(name, 5, 2)
	got = um.GetIndexerUsage(name)
	if got.AllTimeAPIHitsUsed != 25 || got.AllTimeDownloadsUsed != 13 {
		t.Fatalf("expected same-day update to add only positive deltas, got hits=%d downloads=%d", got.AllTimeAPIHitsUsed, got.AllTimeDownloadsUsed)
	}
}
