package stremio

import (
	"testing"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
)

func seedRel(title string, seeders int, known bool) *release.Release {
	return &release.Release{Title: title, Protocol: "torrent", Seeders: seeders, SeedersKnown: known}
}

func seederFilterServer(min int) *Server {
	return &Server{config: &config.Config{MinSeeders: &min}}
}

// TestSeederFilterDisabledByDefault: with no minimum configured nothing is
// dropped, so existing setups behave exactly as before.
func TestSeederFilterDisabledByDefault(t *testing.T) {
	s := seederFilterServer(0)
	in := []*release.Release{seedRel("a", 0, true), seedRel("b", 5, true)}
	if got := s.filterLowSeeders("test", in); len(got) != 2 {
		t.Fatalf("filter must be off by default, got %d of 2", len(got))
	}
}

// TestSeederFilterDropsThinSwarms removes releases below the configured bar.
func TestSeederFilterDropsThinSwarms(t *testing.T) {
	s := seederFilterServer(5)
	in := []*release.Release{
		seedRel("dead", 0, true),
		seedRel("thin", 2, true),
		seedRel("healthy", 40, true),
	}
	got := s.filterLowSeeders("test", in)
	if len(got) != 1 || got[0].Title != "healthy" {
		t.Fatalf("expected only the healthy release to survive, got %d: %+v", len(got), got)
	}
}

// TestSeederFilterIgnoresUnreportedCounts is the safety net: a tracker that does
// not publish seeder counts must not have all of its results filtered away.
func TestSeederFilterIgnoresUnreportedCounts(t *testing.T) {
	s := seederFilterServer(10)
	in := []*release.Release{
		seedRel("no-report-1", 0, false),
		seedRel("no-report-2", 0, false),
	}
	if got := s.filterLowSeeders("test", in); len(got) != 2 {
		t.Fatalf("releases without a reported seeder count must survive, got %d of 2", len(got))
	}
}

// TestSeederFilterOffersNothingRatherThanAnUnplayableList: when no release meets
// the minimum, none are offered.
//
// Keeping them as a last resort listed streams that the play-time guard rejects
// on the very same seeder count, so the viewer chose one and got an error. An
// empty list is the honest answer, and it reads as "nothing available" rather
// than as a broken addon.
func TestSeederFilterOffersNothingRatherThanAnUnplayableList(t *testing.T) {
	s := seederFilterServer(100)
	in := []*release.Release{seedRel("a", 1, true), seedRel("b", 2, true)}
	if got := s.filterLowSeeders("test", in); len(got) != 0 {
		t.Fatalf("no release meets the minimum, so none should be offered, got %d", len(got))
	}
}
