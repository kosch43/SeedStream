package torrent

import (
	"testing"
	"time"

	"seedstream/pkg/core/config"
)

func minSeedersPtr(n int) *int { return &n }

// TestDefaultMinSeedersIsTen pins the shipped default: an operator who has never
// touched the setting still gets the protection.
func TestDefaultMinSeedersIsTen(t *testing.T) {
	if got := (&config.Config{}).EffectiveMinSeeders(); got != 10 {
		t.Fatalf("default minimum seeders = %d, want 10", got)
	}
	if got := (*config.Config)(nil).EffectiveMinSeeders(); got != 10 {
		t.Fatalf("nil config minimum seeders = %d, want 10", got)
	}
}

// TestExplicitZeroDisables: 0 must be distinguishable from "unset", which is
// why the field is a pointer.
func TestExplicitZeroDisables(t *testing.T) {
	cfg := &config.Config{MinSeeders: minSeedersPtr(0)}
	if got := cfg.EffectiveMinSeeders(); got != 0 {
		t.Fatalf("explicit 0 must disable the check, got %d", got)
	}
	cfg = &config.Config{MinSeeders: minSeedersPtr(25)}
	if got := cfg.EffectiveMinSeeders(); got != 25 {
		t.Fatalf("explicit 25 should be honoured, got %d", got)
	}
}

// TestUnderSeededFlagsThinSwarm: an incomplete torrent whose swarm is below the
// floor and which has gone quiet is acted on at half the stall threshold, so
// Cerberus swaps it out well before the full timeout.
func TestUnderSeededFlagsThinSwarm(t *testing.T) {
	threshold := 10 * time.Minute
	e := TorrentHealthEntry{
		State: "downloading", Progress: 0.1, NumSeeds: 2,
		AddedAt:      time.Now().Add(-time.Hour),
		LastActivity: time.Now().Add(-6 * time.Minute), // past half the threshold
	}
	if !isUnderSeeded(e, 10, threshold) {
		t.Fatal("2 seeders with no activity for 6 minutes should be flagged at a 10-minute threshold")
	}
	// The ordinary stall check would NOT have fired yet — this is the earlier
	// reaction the swarm size buys us.
	if isStalled(e, threshold) {
		t.Fatal("precondition: the plain stall check should not fire this early")
	}
}

// TestUnderSeededRespectsGracePeriod: a freshly added torrent legitimately
// reports zero seeds while it announces, and must not be judged yet.
func TestUnderSeededRespectsGracePeriod(t *testing.T) {
	e := TorrentHealthEntry{
		State: "downloading", Progress: 0, NumSeeds: 0,
		AddedAt:      time.Now(),
		LastActivity: time.Now(),
	}
	if isUnderSeeded(e, 10, 10*time.Minute) {
		t.Fatal("a torrent added seconds ago must be given time to find peers")
	}
}

// TestUnderSeededIgnoresHealthyAndCompleted.
func TestUnderSeededIgnoresHealthyAndCompleted(t *testing.T) {
	threshold := 10 * time.Minute
	healthy := TorrentHealthEntry{
		State: "downloading", Progress: 0.1, NumSeeds: 40,
		AddedAt:      time.Now().Add(-time.Hour),
		LastActivity: time.Now().Add(-30 * time.Minute),
	}
	if isUnderSeeded(healthy, 10, threshold) {
		t.Fatal("a well-seeded torrent must never be flagged as under-seeded")
	}
	done := TorrentHealthEntry{
		State: "uploading", Progress: 1, NumSeeds: 0,
		AddedAt:      time.Now().Add(-time.Hour),
		LastActivity: time.Now().Add(-time.Hour),
	}
	if isUnderSeeded(done, 10, threshold) {
		t.Fatal("a completed torrent has nothing left to download and must not be flagged")
	}
}

// TestUnderSeededDisabled: minimum 0 switches the watchdog behaviour off.
func TestUnderSeededDisabled(t *testing.T) {
	e := TorrentHealthEntry{
		State: "downloading", Progress: 0, NumSeeds: 0,
		AddedAt:      time.Now().Add(-time.Hour),
		LastActivity: time.Now().Add(-time.Hour),
	}
	if isUnderSeeded(e, 0, 10*time.Minute) {
		t.Fatal("minimum 0 must disable the under-seeded check")
	}
}

// TestUnderSeededJudgesTheSwarmNotTheConnections is the false-positive guard.
// BitTorrent connects to a subset of a swarm, so 4 connected seeds out of 60 is
// ordinary. Reading the connected count as the swarm size would flag a perfectly
// healthy torrent and have Cerberus replace it with something worse.
func TestUnderSeededJudgesTheSwarmNotTheConnections(t *testing.T) {
	e := TorrentHealthEntry{
		State: "downloading", Progress: 0.1,
		NumSeeds: 4, SwarmSeeds: 60, SwarmKnown: true,
		AddedAt:      time.Now().Add(-time.Hour),
		LastActivity: time.Now().Add(-6 * time.Minute),
	}
	if isUnderSeeded(e, 10, 10*time.Minute) {
		t.Fatal("a 60-seed swarm reached through 4 connections is healthy, not under-seeded")
	}
}

// TestUnderSeededTrustsTheTrackerOverTheConnectionCount: the reverse case. Lots
// of connections cannot rescue a swarm the tracker says is below the floor.
func TestUnderSeededTrustsTheTrackerOverTheConnectionCount(t *testing.T) {
	e := TorrentHealthEntry{
		State: "downloading", Progress: 0.1,
		NumSeeds: 12, SwarmSeeds: 3, SwarmKnown: true,
		AddedAt:      time.Now().Add(-time.Hour),
		LastActivity: time.Now().Add(-6 * time.Minute),
	}
	if !isUnderSeeded(e, 10, 10*time.Minute) {
		t.Fatal("the tracker reports 3 seeders against a floor of 10; that is under-seeded")
	}
}
