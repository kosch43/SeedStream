package triage

import (
	"testing"

	"seedstream/pkg/release"
)

func rel(sizeGB float64, seeders int, known bool) *release.Release {
	return &release.Release{
		Title:        "Thing.2021.1080p",
		Protocol:     "torrent",
		Size:         int64(sizeGB * float64(int64(1)<<30)),
		Seeders:      seeders,
		SeedersKnown: known,
	}
}

// TestSeederHealthActuallyInfluencesRanking is the point of the change. Seeders
// used to matter only on an exact score tie, which effectively never happened,
// so a barely-seeded release could sit above a well-seeded one of similar
// quality and then stall.
func TestSeederHealthActuallyInfluencesRanking(t *testing.T) {
	thin := ScoreRelease(rel(12, 3, true))      // 12 GB, 3 seeders
	healthy := ScoreRelease(rel(12, 200, true)) // same size, healthy swarm

	if healthy <= thin {
		t.Fatalf("a well-seeded release must outscore an identical thin one: %d vs %d", healthy, thin)
	}
}

// TestQualityStillLeads: swarm health must not let a small release beat a much
// larger one outright. Seeders are worth a few size tiers, not unlimited.
func TestQualityStillLeads(t *testing.T) {
	bigModest := ScoreRelease(rel(60, 12, true))    // 60 GB remux, 12 seeders
	smallHuge := ScoreRelease(rel(0.7, 5000, true)) // 700 MB rip, huge swarm

	if smallHuge >= bigModest {
		t.Fatalf("a tiny release must not outrank a remux on seeders alone: %d vs %d", smallHuge, bigModest)
	}
}

// TestSeederScoreIsDiminishing: going from 1 to 10 seeders should matter far
// more than going from 500 to 1000, because that is where stalling is decided.
func TestSeederScoreIsDiminishing(t *testing.T) {
	lowGain := seederScore(rel(10, 10, true)) - seederScore(rel(10, 1, true))
	highGain := seederScore(rel(10, 1000, true)) - seederScore(rel(10, 500, true))

	if lowGain <= highGain {
		t.Fatalf("early seeders should count for more: 1->10 gained %d, 500->1000 gained %d", lowGain, highGain)
	}
}

// TestSeederScoreIsBounded keeps an enormous swarm from swamping quality.
func TestSeederScoreIsBounded(t *testing.T) {
	if got := seederScore(rel(10, 1000000, true)); got != maxSeederScore {
		t.Fatalf("seeder score should cap at %d, got %d", maxSeederScore, got)
	}
}

// TestUnreportedSeedersScoreZero is the safety net: an indexer that omits the
// seeders attribute must not have all of its releases pushed down.
func TestUnreportedSeedersScoreZero(t *testing.T) {
	if got := seederScore(rel(10, 0, false)); got != 0 {
		t.Fatalf("unreported seeders must contribute nothing, got %d", got)
	}
	// And it must score the same as any other release of that size, not less.
	if ScoreRelease(rel(10, 0, false)) != basicScore(rel(10, 0, false)) {
		t.Fatal("a release with no reported count must keep its quality score intact")
	}
}

// TestDeadSwarmStillSinks: the ordering rule that puts unplayable torrents last
// must survive the scoring change.
func TestDeadSwarmStillSinks(t *testing.T) {
	dead := Candidate{Release: rel(100, 0, true), Score: ScoreRelease(rel(100, 0, true))}
	alive := Candidate{Release: rel(1, 20, true), Score: ScoreRelease(rel(1, 20, true))}

	if !moreDesirable(&alive, &dead) {
		t.Fatal("a confirmed dead swarm must sort below anything playable regardless of score")
	}
}
