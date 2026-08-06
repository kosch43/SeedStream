package torrent

import (
	"fmt"
	"testing"

	"seedstream/pkg/torrent/qbittorrent"
)

// The strings that produced the failure, verbatim from a real seedbox. The
// indexer publishes a normalised release name; qBittorrent reports the name
// inside the .torrent. They describe the same file, with the tags in a
// different order and HDR10+ spelled two ways.
const (
	fieldQbitName     = "The.Super.Mario.Galaxy.Movie.2026.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HDR10P.HEVC.HYBRID.REMUX-FraMeSToR.mkv"
	fieldIndexerTitle = "The Super Mario Galaxy Movie 2026 Hybrid 2160p UHD BluRay REMUX DV HDR10+ HEVC TrueHD 7 1 Atmos-FraMeSToR"
)

// TestReorderedTagsStillMatch is the regression. Because normalisation joins
// words with no delimiter, reordering the tags produced a completely different
// string, so SeedStream added a torrent and then could not recognise its own
// work. It reported the candidate failed, fell back to another release, and
// left two 59 GB remuxes of the same film downloading side by side.
func TestReorderedTagsStillMatch(t *testing.T) {
	list := []qbittorrent.TorrentInfo{{
		Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Name: fieldQbitName, Progress: 0.4,
	}}
	got := exactTitleMatch(list, fieldIndexerTitle)
	if got == nil {
		t.Fatal("the same release written with its tags in a different order must still match itself")
	}
}

// TestHDR10PlusSpellingsAgree isolates the second difference. Punctuation is
// stripped during normalisation, so "HDR10+" would arrive as bare "hdr10" —
// which is a genuinely different format from HDR10+ — while "HDR10P" arrives as
// "hdr10p". The plus is preserved as a "p" rather than dropped, so the two
// spellings agree without collapsing HDR10 into HDR10+.
func TestHDR10PlusSpellingsAgree(t *testing.T) {
	base := "Some.Film.2024.2160p.REMUX.DV.%s.HEVC-GRP"
	for _, pair := range [][2]string{
		{"HDR10+", "HDR10P"},
		{"HDR10+", "HDR10Plus"},
		{"HDR10P", "HDR10Plus"},
	} {
		a := []qbittorrent.TorrentInfo{{Name: fmt.Sprintf(base, pair[0]), Progress: 1}}
		if exactTitleMatch(a, fmt.Sprintf(base, pair[1])) == nil {
			t.Errorf("%q and %q describe the same format and must match", pair[0], pair[1])
		}
	}
	// ...but plain HDR10 is a different format and must NOT be folded in.
	plain := []qbittorrent.TorrentInfo{{Name: fmt.Sprintf(base, "HDR10"), Progress: 1}}
	if exactTitleMatch(plain, fmt.Sprintf(base, "HDR10+")) != nil {
		t.Error("HDR10 and HDR10+ are different formats and must not be treated as the same release")
	}
}

// TestSiblingEpisodesStillDoNotMatch is the strictness that must survive.
// Comparing word multisets rather than the joined string tolerates reordering,
// and the reason that is safe is that a different episode is a different word.
// Losing this would let a replay of one episode resolve to another.
func TestSiblingEpisodesStillDoNotMatch(t *testing.T) {
	list := []qbittorrent.TorrentInfo{{
		Name: "Some.Show.S05E02.1080p.WEB-DL.DDP5.1.H.264-GRP", Progress: 1,
	}}
	if got := exactTitleMatch(list, "Some.Show.S05E01.1080p.WEB-DL.DDP5.1.H.264-GRP"); got != nil {
		t.Fatalf("S05E01 must not resolve to S05E02, matched %q", got.Name)
	}
}

// TestDifferentReleaseGroupsDoNotMatch: same film, same tags, different group
// is a different file. One word apart must still be a mismatch.
func TestDifferentReleaseGroupsDoNotMatch(t *testing.T) {
	list := []qbittorrent.TorrentInfo{{
		Name: "Some.Film.2024.2160p.UHD.BluRay.REMUX-FraMeSToR", Progress: 1,
	}}
	if got := exactTitleMatch(list, "Some.Film.2024.2160p.UHD.BluRay.REMUX-OTHERGRP"); got != nil {
		t.Fatalf("a different release group is a different file, matched %q", got.Name)
	}
}

// TestExtraTagIsNotAMatch: a superset of the words is not the same release.
// Sorting must not turn the comparison into "contains".
func TestExtraTagIsNotAMatch(t *testing.T) {
	list := []qbittorrent.TorrentInfo{{
		Name: "Some.Film.2024.2160p.UHD.BluRay.REMUX.PROPER-GRP", Progress: 1,
	}}
	if got := exactTitleMatch(list, "Some.Film.2024.2160p.UHD.BluRay.REMUX-GRP"); got != nil {
		t.Fatalf("a PROPER is a different release, matched %q", got.Name)
	}
}

// TestExactMatchPrefersTheMoreCompleteCopy keeps the existing preference.
func TestExactMatchPrefersTheMoreCompleteCopy(t *testing.T) {
	list := []qbittorrent.TorrentInfo{
		{Hash: "aaa", Name: fieldQbitName, Progress: 0.2},
		{Hash: "bbb", Name: fieldQbitName, Progress: 0.9},
	}
	got := exactTitleMatch(list, fieldIndexerTitle)
	if got == nil || got.Hash != "bbb" {
		t.Fatalf("expected the more complete copy, got %+v", got)
	}
}
