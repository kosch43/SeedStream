package stremio

import (
	"fmt"

	"seedstream/pkg/release"
	"seedstream/pkg/search/parser"
)

// releaseMatchesRequest reports why a release must not be served for the
// requested content, or nil when it is a legitimate match.
//
// This is the last line of defence against serving the wrong title. Search-time
// validation (pkg/search/validation.go) only filters by title when TMDB supplied
// an expected title — when that lookup fails, validation silently disables
// itself and unrelated releases stay in the playlist. That is how a request for
// a series episode could end up serving an unrelated film.
//
// For an episode request the check is exact and needs no external metadata: the
// release title must parse to something that can actually contain the requested
// season/episode. Season packs and complete-series packs legitimately do, so
// they are accepted; an unrelated film parses to neither and is rejected.
func releaseMatchesRequest(rel *release.Release, season, episode int) error {
	if rel == nil {
		return fmt.Errorf("no release attached to session")
	}
	// Only episode requests can be verified without external metadata. Movie
	// requests carry no season/episode to check against, so they are left to
	// search-time validation.
	if season <= 0 || episode <= 0 {
		return nil
	}
	parsed := parser.ParseReleaseTitle(rel.Title)
	if parsed == nil {
		return fmt.Errorf("could not parse release title %q", rel.Title)
	}
	if !parsed.MatchesEpisodeRequest(season, episode) {
		return fmt.Errorf("release %q does not contain S%02dE%02d", rel.Title, season, episode)
	}
	return nil
}

// releaseHasEnoughSeeders reports why a release's swarm is too thin to stream,
// or nil when it is healthy enough.
//
// A torrent with only a handful of seeders downloads slower than playback
// consumes it, so it either never finishes buffering or stalls partway through.
// Refusing it here — before it is handed to qBittorrent — turns a guaranteed
// buffering session into an immediate failover to a better-seeded release.
//
// Only a count the tracker actually published is judged. Indexers that omit the
// seeders attribute report 0 indistinguishably from a genuinely dead swarm, so
// those releases pass through rather than being wrongly rejected.
func releaseHasEnoughSeeders(rel *release.Release, minSeeders int) error {
	if minSeeders <= 0 || rel == nil || !rel.IsTorrent() {
		return nil
	}
	if !rel.SeedersKnown {
		return nil
	}
	if rel.Seeders < minSeeders {
		return fmt.Errorf("only %d seeder(s), need at least %d to stream without stalling", rel.Seeders, minSeeders)
	}
	return nil
}
