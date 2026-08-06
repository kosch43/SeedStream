package stremio

import (
	"seedstream/pkg/core/logger"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// prebufferSeconds is how much playback time must be on disk before a stream
// starts. Expressing the head buffer in seconds rather than bytes is what makes
// high-bitrate content work: a fixed byte count is a completely different amount
// of video depending on the bitrate.
const prebufferSeconds = 25

// maxPrebufferBytes caps the head buffer so startup never waits absurdly long on
// an extreme bitrate, and so the prepare budget is not consumed by buffering.
const maxPrebufferBytes int64 = 384 * 1024 * 1024

// playbackBufferBytes returns how much of the file's head must be downloaded
// before playback starts, sized so it holds roughly prebufferSeconds of video.
//
// The default 16 MiB buffer is fine for ordinary web releases but nearly nothing
// for a remux: at 5 Mbps it is 27 seconds of video, at 67 Mbps barely 2. Starting
// a remux on a two second cushion means the player outruns the download almost
// immediately and rebuffers, which is why large remuxes appear not to stream
// even when the swarm is healthy.
//
// Returns 0 when the size or runtime is unknown, leaving the caller's configured
// default in place, and never returns less than that default — this only ever
// enlarges the buffer for content that needs it.
func (s *Server) playbackBufferBytes(sess *session.Session, configured int64) int64 {
	if sess == nil || sess.Release == nil {
		return 0
	}
	sizeBytes := sess.Release.Size
	if sizeBytes <= 0 {
		return 0
	}
	isSeries := sess.ContentIDs != nil && (sess.ContentIDs.Season > 0 || sess.ContentIDs.Episode > 0)
	runtimeSec := s.resolveRuntimeSeconds(sess)
	if runtimeSec <= 0 {
		runtimeSec = nominalRuntimeSeconds(isSeries)
	}
	if runtimeSec <= 0 {
		return 0
	}

	want := sizeBytes * prebufferSeconds / runtimeSec
	if want > maxPrebufferBytes {
		want = maxPrebufferBytes
	}
	// Never buffer a large fraction of a small file — the whole thing would have
	// to download before playback could start.
	if half := sizeBytes / 2; want > half {
		want = half
	}
	if configured <= 0 {
		configured = torrent.DefaultBufferBytes
	}
	if want <= configured {
		return 0 // ordinary bitrate: the configured default is already enough
	}
	bitrateMbps := float64(sizeBytes) * 8 / float64(runtimeSec) / 1e6
	logger.Debug("Playback: enlarging head buffer for high-bitrate release",
		"session", sess.ID, "bitrate_mbps", bitrateMbps,
		"buffer_bytes", want, "seconds_of_video", prebufferSeconds)
	return want
}
