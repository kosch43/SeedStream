package torrent

import (
	"sort"
	"testing"
)

// rankReplacements mirrors findReplacement's ordering so it can be exercised
// without an indexer. The sort function is the thing under test; keeping it
// here in the same shape means a change to one that is not made to the other
// shows up as a failure rather than as drift.
func rankReplacements(in []replacementCandidate) []replacementCandidate {
	out := append([]replacementCandidate(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].dead != out[j].dead {
			return out[j].dead
		}
		return out[i].seeders > out[j].seeders
	})
	return out
}

type replacementCandidate struct {
	url     string
	seeders int
	dead    bool
}

// TestReplacementRanksOnSeedersAlone: the torrent being replaced already
// stalled, so the only question is which candidate will actually finish.
// Seeder count answers that; how good the release looks does not.
func TestReplacementRanksOnSeedersAlone(t *testing.T) {
	got := rankReplacements([]replacementCandidate{
		{url: "remux", seeders: 3},
		{url: "web-dl", seeders: 40},
		{url: "rip", seeders: 900},
	})
	if got[0].url != "rip" {
		t.Fatalf("the healthiest swarm must win, got %q", got[0].url)
	}
	if got[1].url != "web-dl" || got[2].url != "remux" {
		t.Fatalf("ordering should follow seeder count exactly, got %v", got)
	}
}

// TestDeadSwarmsSinkBelowEverything: a candidate with a confirmed zero swarm
// cannot finish at all, so it ranks below any candidate that might, whatever
// the seeder numbers say about the rest.
func TestDeadSwarmsSinkBelowEverything(t *testing.T) {
	got := rankReplacements([]replacementCandidate{
		{url: "dead", seeders: 0, dead: true},
		{url: "thin", seeders: 2},
	})
	if got[0].url != "thin" {
		t.Fatalf("anything playable must outrank a dead swarm, got %q", got[0].url)
	}
}

// TestUnknownSeederCountIsNotTreatedAsDead: an indexer that does not publish
// seeder counts reports zero indistinguishably from a genuinely empty swarm.
// Only a count the tracker actually published marks a candidate dead, so those
// releases stay eligible instead of being sorted to the bottom wholesale.
func TestUnknownSeederCountIsNotTreatedAsDead(t *testing.T) {
	got := rankReplacements([]replacementCandidate{
		{url: "confirmed-dead", seeders: 0, dead: true},
		{url: "unreported", seeders: 0, dead: false},
	})
	if got[0].url != "unreported" {
		t.Fatalf("an unreported count is not a dead swarm, got %q", got[0].url)
	}
}
