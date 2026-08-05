package stremio

import (
	"context"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/release"
	"seedstream/pkg/services/cerberus"
)

// resolvePlayRelease decides which torrent actually gets played for this
// request. It returns the session's own release unchanged in the normal case,
// or a copy pointed at a torrent the seedbox already holds.
//
// SeedStream registers every torrent it hands to qBittorrent against the content
// it was played for, so it remembers what has already been downloaded. Replaying
// a title should not re-download it: search ranking is not stable, so a later
// visit can select a different release and start a fresh download even though a
// complete copy of the same episode is sitting on the seedbox.
//
// Precedence deliberately keeps the viewer's choice first:
//  1. If the selected release is already on a seedbox, use it — nothing to gain
//     from switching, and it plays immediately anyway.
//  2. Otherwise, if a torrent previously played for this exact content is still
//     present, play that instead of downloading something new. Complete copies
//     win over partial ones, then most recent.
//  3. Otherwise, fall through to the normal add-and-buffer path.
//
// Reuse candidates come from the registry keyed by season, episode and content
// ID, and are re-checked against the episode guard, so a remembered torrent can
// never be for a different title than the one requested.
func (s *Server) resolvePlayRelease(ctx context.Context, sel *release.Release, selectedHash string, ids cerberus.ContentIDs, season, episode int) *release.Release {
	if s.cerberusClient == nil || s.torrentManager == nil || sel == nil {
		return sel
	}
	if ids.ImdbID == "" && ids.TmdbID == "" && ids.TvdbID == "" {
		return sel
	}

	// 1. The chosen release is already downloaded (or downloading) — keep it.
	if selectedHash != "" {
		if present := s.torrentManager.FindTorrent(ctx, selectedHash); present != nil {
			logger.Debug("Play: selected release already on seedbox, reusing it",
				"hash", selectedHash, "progress", present.Progress, "client", present.ClientName)
			return sel
		}
	}

	// 2. Look for a torrent already downloaded for this exact content.
	known := s.cerberusClient.KnownTorrentsForContent(ids)
	var best *cerberus.TorrentRecord
	var bestPresence *torrentPresence
	for i := range known {
		rec := &known[i]
		if rec.InfoHash == "" || rec.InfoHash == selectedHash {
			continue
		}
		// Never replay something that is not the requested episode.
		if err := releaseMatchesRequest(&release.Release{Title: rec.ReleaseTitle}, season, episode); err != nil {
			continue
		}
		present := s.torrentManager.FindTorrent(ctx, rec.InfoHash)
		if present == nil {
			continue // registered once, but no longer on any seedbox
		}
		cand := &torrentPresence{complete: present.Complete(), progress: present.Progress, client: present.ClientName}
		if best == nil || (cand.complete && !bestPresence.complete) {
			best, bestPresence = rec, cand
		}
		if bestPresence.complete {
			break // a complete copy is the best possible outcome; stop looking
		}
	}
	if best == nil {
		return sel
	}

	logger.Info("Play: replaying from a torrent the seedbox already has",
		"requested_release", sel.Title, "reused_release", best.ReleaseTitle,
		"hash", best.InfoHash, "complete", bestPresence.complete,
		"progress", bestPresence.progress, "client", bestPresence.client)

	reused := *sel
	reused.Title = best.ReleaseTitle
	reused.InfoHash = best.InfoHash
	reused.Magnet = best.Magnet
	reused.Protocol = "torrent"
	return &reused
}

// torrentPresence is the small subset of presence data the choice needs.
type torrentPresence struct {
	complete bool
	progress float64
	client   string
}
