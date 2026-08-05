package triage

import (
	"testing"

	"seedstream/pkg/release"
)

func torrentRel(title string, seeders int, known bool) *release.Release {
	return &release.Release{Title: title, Protocol: "torrent", Seeders: seeders, SeedersKnown: known}
}

// TestDeadSwarmSortsBelowPlayable is the ranking fix: a confirmed zero-seeder
// torrent must sort below a playable one even when its quality score is higher.
// basicScore rewards size and recency, so a freshly posted 4K remux with no
// seeders used to top the list and stall on playback.
func TestDeadSwarmSortsBelowPlayable(t *testing.T) {
	dead := Candidate{Release: torrentRel("Big.4K.REMUX", 0, true), Score: 9000}
	alive := Candidate{Release: torrentRel("Modest.1080p", 50, true), Score: 3000}

	if !moreDesirable(&alive, &dead) {
		t.Fatal("a seeded release must outrank a confirmed dead one despite a lower score")
	}
	if moreDesirable(&dead, &alive) {
		t.Fatal("a dead release must not outrank a seeded one")
	}
}

// TestUnreportedSeedersNotTreatedAsDead is the safety net: an indexer that does
// not publish a seeder count reads as 0, and must never be penalised for it —
// otherwise every result from such a tracker would sink.
func TestUnreportedSeedersNotTreatedAsDead(t *testing.T) {
	unreported := Candidate{Release: torrentRel("Unknown.Health.1080p", 0, false), Score: 9000}
	alive := Candidate{Release: torrentRel("Seeded.1080p", 50, true), Score: 3000}

	if !moreDesirable(&unreported, &alive) {
		t.Fatal("a release with no reported seeder count must keep its score-based rank")
	}
	if isDeadSwarm(unreported.Release) {
		t.Fatal("an unreported seeder count must not count as a dead swarm")
	}
}

// TestSeedersStillBreakScoreTies keeps the existing tiebreaker behaviour.
func TestSeedersStillBreakScoreTies(t *testing.T) {
	a := Candidate{Release: torrentRel("A", 100, true), Score: 5000}
	b := Candidate{Release: torrentRel("B", 10, true), Score: 5000}

	if !moreDesirable(&a, &b) {
		t.Fatal("on equal scores the better-seeded release should win")
	}
}

// TestBothDeadFallsBackToScore: when neither can be played, ordinary quality
// ordering still applies.
func TestBothDeadFallsBackToScore(t *testing.T) {
	high := Candidate{Release: torrentRel("High", 0, true), Score: 9000}
	low := Candidate{Release: torrentRel("Low", 0, true), Score: 1000}

	if !moreDesirable(&high, &low) {
		t.Fatal("between two dead releases the higher score should still sort first")
	}
}
