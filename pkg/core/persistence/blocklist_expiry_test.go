package persistence

import (
	"testing"
	"time"
)

// freshManager gives a test its own database. GetManager is a process singleton,
// so without resetting it a test inherits whichever directory was opened first —
// and leaves the singleton pointing at its own temp dir for whatever runs next.
func freshManager(t *testing.T) (*StateManager, error) {
	t.Helper()
	globalManager = nil
	t.Cleanup(func() { globalManager = nil })
	return GetManager(t.TempDir())
}

// TestBlocklistExpiry: a swarm that was dead last month may be healthy now, so
// blocklist entries must age out rather than banning a torrent forever.
func TestBlocklistExpiry(t *testing.T) {
	m, err := freshManager(t)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	const oldHash = "1111111111111111111111111111111111111111"
	const freshHash = "2222222222222222222222222222222222222222"

	if err := m.ReportTorrentFailure(oldHash, "stalled"); err != nil {
		t.Fatalf("report old: %v", err)
	}
	if err := m.ReportTorrentFailure(freshHash, "stalled"); err != nil {
		t.Fatalf("report fresh: %v", err)
	}
	// Backdate one entry by 60 days.
	old := time.Now().Add(-60 * 24 * time.Hour).UnixMilli()
	if _, err := m.db.Exec(`UPDATE cerberus_blocklist SET last_failure_at = ? WHERE info_hash = ?`, old, oldHash); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	n, err := m.PruneOldBlocklistEntries(30 * 24 * 60 * 60 * 1000)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly the stale entry to expire, removed %d", n)
	}
	if m.IsInfoHashBlocked(oldHash) {
		t.Error("the 60-day-old entry should have expired")
	}
	if !m.IsInfoHashBlocked(freshHash) {
		t.Error("a recent entry must not be expired")
	}
}

// TestBlocklistExpiryDisabled: a non-positive age keeps entries forever.
func TestBlocklistExpiryDisabled(t *testing.T) {
	m, err := freshManager(t)
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	if n, err := m.PruneOldBlocklistEntries(0); err != nil || n != 0 {
		t.Fatalf("zero age must be a no-op, got n=%d err=%v", n, err)
	}
}
