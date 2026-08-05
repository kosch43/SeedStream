package stremio

import (
	"testing"

	"seedstream/pkg/release"
)

// TestReleaseMatchesRequestRejectsUnrelatedTitle is the regression test for the
// wrong-content bug: a request for Rick and Morty S05E01 was served an
// unrelated film because search-time title validation had silently disabled
// itself (no TMDB expectation available).
func TestReleaseMatchesRequestRejectsUnrelatedTitle(t *testing.T) {
	rel := &release.Release{Title: "The.Shawshank.Redemption.1994.REPACK.1080p.BluRay.x265-YAWNTiC"}
	if err := releaseMatchesRequest(rel, 5, 1); err == nil {
		t.Fatal("an unrelated film must not satisfy a request for S05E01")
	}
}

// TestReleaseMatchesRequestAcceptsLegitimateForms: exact episodes, season packs
// and complete-series packs all legitimately contain the requested episode and
// must keep working — the guard must not break season-pack playback.
func TestReleaseMatchesRequestAcceptsLegitimateForms(t *testing.T) {
	for _, title := range []string{
		"Rick.and.Morty.S05E01.Mort.Dinner.Rick.Andre.1080p.WEBRip.x265-RARBG",
		"Rick.and.Morty.S05.1080p.WEBRip.x265-RARBG",
		"Rick.and.Morty.S01-S05.COMPLETE.1080p.WEBRip.x265",
	} {
		if err := releaseMatchesRequest(&release.Release{Title: title}, 5, 1); err != nil {
			t.Errorf("%q should satisfy S05E01: %v", title, err)
		}
	}
}

// TestReleaseMatchesRequestRejectsWrongEpisode guards against serving a
// different episode of the right show.
func TestReleaseMatchesRequestRejectsWrongEpisode(t *testing.T) {
	rel := &release.Release{Title: "Rick.and.Morty.S03E07.1080p.WEBRip.x265-RARBG"}
	if err := releaseMatchesRequest(rel, 5, 1); err == nil {
		t.Fatal("S03E07 must not satisfy a request for S05E01")
	}
}

// TestReleaseMatchesRequestSkipsMovieRequests: a movie request carries no
// season/episode, so there is nothing to verify here and the guard must not
// reject it.
func TestReleaseMatchesRequestSkipsMovieRequests(t *testing.T) {
	rel := &release.Release{Title: "The.Shawshank.Redemption.1994.REPACK.1080p.BluRay.x265-YAWNTiC"}
	if err := releaseMatchesRequest(rel, 0, 0); err != nil {
		t.Fatalf("movie requests must pass through the episode guard: %v", err)
	}
}

// TestReleaseMatchesRequestNilRelease guards the nil path.
func TestReleaseMatchesRequestNilRelease(t *testing.T) {
	if err := releaseMatchesRequest(nil, 5, 1); err == nil {
		t.Fatal("a nil release must be rejected")
	}
}
