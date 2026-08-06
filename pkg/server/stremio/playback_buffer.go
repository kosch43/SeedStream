package stremio

import (
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

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

// playbackProfile describes the release for the head calculation: how many
// bytes it is and how long it plays for. Zero when either is unknown.
func (s *Server) playbackProfile(sess *session.Session) torrent.PlaybackProfile {
	if sess == nil || sess.Release == nil || sess.Release.Size <= 0 {
		return torrent.PlaybackProfile{}
	}
	isSeries := sess.ContentIDs != nil && (sess.ContentIDs.Season > 0 || sess.ContentIDs.Episode > 0)
	runtimeSec := s.resolveRuntimeSeconds(sess)
	if runtimeSec <= 0 {
		runtimeSec = nominalRuntimeSeconds(isSeries)
	}
	if runtimeSec <= 0 {
		return torrent.PlaybackProfile{}
	}
	return torrent.PlaybackProfile{FileBytes: sess.Release.Size, RuntimeSeconds: runtimeSec}
}

// playbackBufferBytes returns the head to require before playback starts.
//
// This is the opening figure only. The download rate is unknowable here — the
// torrent has not been added, let alone found peers — so it rests on the
// bitrate prior, and prepare revises it downward once a real rate is in hand.
//
// Returns 0 when the size or runtime is unknown, leaving the caller's
// configured default in place: there is nothing to compute from.
//
// configuredFloor is the operator's explicit torrent_buffer_bytes setting, or 0
// when they have not set one. An explicit setting is honoured as a floor; the
// built-in default is not, because being unable to go below it is what made
// small releases wait far longer than their bitrate warranted.
func (s *Server) playbackBufferBytes(sess *session.Session, configuredFloor int64) int64 {
	profile := s.playbackProfile(sess)
	if !profile.Valid() {
		return 0
	}
	// Rate 0: not known yet at this point in the request.
	want := torrent.HeadBytesFor(profile, 0)

	// An operator who set a buffer explicitly meant it.
	if configuredFloor > 0 && want < configuredFloor {
		want = configuredFloor
	}
	if want > profile.FileBytes {
		want = profile.FileBytes
	}

	// Logged at info because this figure decides how long a stream takes to
	// start, and it is the first thing to check when one feels slow.
	logger.Info("Playback: opening head buffer for this release",
		"session", sess.ID,
		"bitrate_mbps", profile.BytesPerSecond()*8/1e6,
		"buffer_bytes", want)
	return want
}
