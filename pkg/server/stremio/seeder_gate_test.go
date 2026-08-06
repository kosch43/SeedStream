package stremio

import (
	"testing"

	"seedstream/pkg/release"
)

func seederRel(seeders int, known bool) *release.Release {
	return &release.Release{
		Title:        "Rick.and.Morty.S05E01.1080p.WEBRip.x265-RARBG",
		Protocol:     "torrent",
		Seeders:      seeders,
		SeedersKnown: known,
	}
}

// TestPlayGateRejectsThinSwarm is the play-time half of the minimum-seeder rule:
// a release the tracker reports as barely seeded must be refused before it is
// handed to qBittorrent, so failover picks a healthier one instead of the
// viewer watching it buffer.
func TestPlayGateRejectsThinSwarm(t *testing.T) {
	for _, seeders := range []int{0, 1, 9} {
		if err := releaseHasEnoughSeeders(seederRel(seeders, true), 10); err == nil {
			t.Errorf("%d seeders must be refused when the minimum is 10", seeders)
		}
	}
}

// TestPlayGateAllowsHealthySwarm: at or above the floor, play proceeds.
func TestPlayGateAllowsHealthySwarm(t *testing.T) {
	for _, seeders := range []int{10, 11, 500} {
		if err := releaseHasEnoughSeeders(seederRel(seeders, true), 10); err != nil {
			t.Errorf("%d seeders should be allowed with a minimum of 10: %v", seeders, err)
		}
	}
}

// TestPlayGateIgnoresUnreportedSeeders is the safety net: an indexer that does
// not publish seeder counts reports 0 indistinguishably from a dead swarm, so
// those releases must not be refused.
func TestPlayGateIgnoresUnreportedSeeders(t *testing.T) {
	if err := releaseHasEnoughSeeders(seederRel(0, false), 10); err != nil {
		t.Fatalf("a release with no reported seeder count must not be refused: %v", err)
	}
}

// TestPlayGateDisabled: a minimum of 0 turns the check off entirely.
func TestPlayGateDisabled(t *testing.T) {
	if err := releaseHasEnoughSeeders(seederRel(0, true), 0); err != nil {
		t.Fatalf("minimum 0 must disable the check: %v", err)
	}
}
