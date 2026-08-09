package stremio

import (
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/release"
	"seedstream/pkg/session"
	"seedstream/pkg/stats"
)

// recordTrackerGrab counts one release as taken from the tracker that supplied
// it, against both the live usage counters and the persisted event log.
//
// Nothing recorded a grab before this. The Torznab client counts one inside
// DownloadNZB, which SeedStream never calls: a torrent is not fetched as a file
// but handed to a download client as a magnet, so that path is dead for every
// tracker. Definition-driven trackers had no counter at all. The result was a
// statistics page whose download columns were structurally zero — not because
// nothing had been grabbed, but because nothing was ever counted.
//
// The moment chosen is the successful prepare: the torrent is by then accepted
// by a download client, which is what taking a release from a tracker means
// here. Recording earlier would count releases that turned out to be
// unplayable; recording at first byte served would count a stream rather than a
// grab, and would miss a torrent that was grabbed and then stalled.
//
// Latched once per session. A player reconnects and re-requests ranges
// throughout playback, and each of those re-enters the play handler, so an
// unguarded counter would report dozens of grabs for one torrent.
func recordTrackerGrab(sess *session.Session, rel *release.Release, um *indexer.UsageManager) {
	if sess == nil || rel == nil {
		return
	}
	name := trackerNameFor(rel, sess)
	if name == "" {
		// A release with no tracker attached — a replay resolved from the
		// registry, say. Counting it against an empty name would create a
		// phantom tracker on the stats page.
		return
	}
	if !sess.MarkDownloadRecorded() {
		return
	}

	if um != nil {
		um.IncrementUsed(name, 0, 1)
	}
	// Success is true because this records a grab that demonstrably worked: the
	// torrent reached a download client. Failures never reach here, so a false
	// would be unreachable rather than merely rare.
	stats.Default().RecordIndexerDownload(name, true, rel.IndexersInSearch, rel.IndexersWithResult)

	logger.Debug("Recorded tracker grab",
		"tracker", name, "session", sess.ID, "release", rel.Title)
}

// trackerNameFor resolves which tracker a played release came from.
//
// The release being played is not always the one the viewer picked: replaying
// from a torrent the seedbox already holds substitutes a different release,
// and that copy carries no tracker of its own. The session's original release
// is the honest answer there — that is the tracker the content was found on.
func trackerNameFor(rel *release.Release, sess *session.Session) string {
	if rel != nil && rel.Indexer != "" {
		return rel.Indexer
	}
	if sess != nil {
		return sess.ReleaseIndexer()
	}
	return ""
}
