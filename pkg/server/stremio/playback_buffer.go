package stremio

import (
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// minPrebufferBytes is the smallest head worth starting on: enough for the
// container header and index plus a few seconds of a modest stream. Below this
// the first read would sit at the edge of the downloaded region and stall
// immediately, so shrinking further buys nothing.
const minPrebufferBytes int64 = 4 * 1024 * 1024

// maxPrebufferBytes caps the head buffer so startup never waits absurdly long on
// an extreme bitrate, and so the prepare budget is not consumed by buffering.
const maxPrebufferBytes int64 = 384 * 1024 * 1024

// prebufferSecondsFor returns how many seconds of video to hold before starting,
// given the release's bitrate in Mbps.
//
// The head buffer exists to cover the window where the player would otherwise
// outrun the download, and how likely that is depends almost entirely on
// bitrate. A 1080p web release at 5 Mbps is outpaced by any seedbox by a factor
// of tens, so a few seconds of cushion is already generous — waiting longer just
// delays the stream for no benefit. A 4K remux at 67 Mbps is in the same league
// as the swarm feeding it, so the margin is thin and the cushion has to be deep.
//
// Scaling the seconds as well as the bytes-per-second is what separates the two:
// a 2 GB episode ends up with single-digit megabytes and a 60 GB remux with
// several hundred, rather than both being anchored to one fixed figure that is
// too slow for the first and too small for the second.
func prebufferSecondsFor(bitrateMbps float64) int64 {
	switch {
	case bitrateMbps < 6: // 1080p x265 / WEB-DL
		return 8
	case bitrateMbps < 16: // 1080p Blu-ray, high-bitrate WEB-DL
		return 15
	case bitrateMbps < 40: // 4K WEB-DL, 1080p remux
		return 30
	default: // 4K remux
		return 45
	}
}

// headFillRate is the download rate a play request is budgeted against when
// deciding how long to allow for buffering. Deliberately well below what a
// seedbox achieves: the budget should be generous enough that a healthy
// download is not cut off, and being wrong in that direction costs a wait,
// while being wrong in the other costs a second copy of the film.
const headFillRate = 8 * 1024 * 1024 // bytes per second

// maxPrepareBudget caps the whole thing. A play request is an HTTP request, and
// reverse proxies in front of SeedStream commonly cut one off between one and
// two minutes; stretching past that produces a proxy error instead of a
// stream. The cap keeps the automatic extension within reach of the retry,
// which is what actually rescues a head too large to fill in one request.
const maxPrepareBudget = 3 * time.Minute

// prepareBudget returns how long a play request may spend getting the stream
// ready, scaled to the head this particular release needs.
//
// A fixed budget was sized when every head was 16 MiB. A 4K remux now asks for
// several hundred, which at any plausible rate cannot be delivered in the same
// ninety seconds — so the request expired every time, and the failover it
// triggered started a second download of the same film alongside the first.
//
// The configured timeout is the floor, never the ceiling: an operator who
// raised it still gets what they asked for.
func (s *Server) prepareBudget(sess *session.Session, headBytes int64) time.Duration {
	base := s.config.EffectiveTorrentPrepareTimeout()
	if headBytes <= 0 {
		return base
	}
	want := base + time.Duration(float64(headBytes)/headFillRate*float64(time.Second))
	if want > maxPrepareBudget {
		want = maxPrepareBudget
	}
	if want < base {
		return base
	}
	if want > base {
		logger.Debug("Playback: extending the prepare budget for a large head",
			"head_bytes", headBytes, "base", base, "budget", want)
	}
	return want
}

// playbackBufferBytes returns how much of the file's head must be downloaded
// before playback starts, scaled to the release's bitrate.
//
// Returns 0 when the size or runtime is unknown, leaving the caller's configured
// default in place — there is nothing to scale from.
//
// configuredFloor is the operator's explicit torrent_buffer_bytes setting, or 0
// when they have not set one. An explicit setting is honoured as a floor; the
// built-in default is not, because being unable to go below it is what made
// small releases wait far longer than their bitrate warranted.
func (s *Server) playbackBufferBytes(sess *session.Session, configuredFloor int64) int64 {
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

	bitrateMbps := float64(sizeBytes) * 8 / float64(runtimeSec) / 1e6
	seconds := prebufferSecondsFor(bitrateMbps)
	want := sizeBytes * seconds / runtimeSec

	if want > maxPrebufferBytes {
		want = maxPrebufferBytes
	}
	// A tenth of the file is minutes of video at any bitrate, so it is always
	// enough headroom to start on. Runtime metadata is occasionally wrong, and
	// this keeps a bad estimate from turning into a long wait.
	if ceiling := int64(float64(sizeBytes) * torrent.StreamableHeadFraction); want > ceiling && ceiling > 0 {
		want = ceiling
	}
	if want < minPrebufferBytes {
		want = minPrebufferBytes
	}
	// An operator who set a buffer explicitly meant it.
	if configuredFloor > 0 && want < configuredFloor {
		want = configuredFloor
	}
	// Never wait on more than the file holds.
	if want > sizeBytes {
		want = sizeBytes
	}

	// Logged at info because this figure decides how long a stream takes to
	// start, and it is the first thing to check when one feels slow.
	logger.Info("Playback: head buffer sized for this release",
		"session", sess.ID, "bitrate_mbps", bitrateMbps,
		"seconds_of_video", seconds, "buffer_bytes", want)
	return want
}
