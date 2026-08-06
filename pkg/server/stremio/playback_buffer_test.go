package stremio

import (
	"testing"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// bufferSession builds a movie session of the given size. tmdbClient is nil so
// the nominal 2h runtime is used, which is what a real lookup failure produces.
func bufferSession(sizeBytes int64) *session.Session {
	return &session.Session{
		ID:         "buf",
		Release:    &release.Release{Title: "Movie.2020.2160p.BluRay.REMUX", Protocol: "torrent", Size: sizeBytes},
		ContentIDs: &session.ContentMeta{ImdbID: "tt0000001"},
	}
}

func bufferServer() *Server { return &Server{config: &config.Config{}} }

// TestRemuxGetsAMuchLargerBuffer is the point of the change: at remux bitrates
// the fixed 16 MiB default is about two seconds of video, so playback starts and
// immediately outruns the download. The buffer must scale with bitrate.
func TestRemuxGetsAMuchLargerBuffer(t *testing.T) {
	s := bufferServer()
	// 60 GB over a nominal 2h ≈ 67 Mbps.
	got := s.playbackBufferBytes(bufferSession(60_000_000_000), torrent.DefaultBufferBytes)

	if got <= torrent.DefaultBufferBytes {
		t.Fatalf("a 60 GB remux must get more than the %d byte default, got %d",
			torrent.DefaultBufferBytes, got)
	}
	// Should be roughly prebufferSeconds of video: 60e9/7200*25 ≈ 208 MB.
	if got < 150_000_000 || got > maxPrebufferBytes {
		t.Fatalf("buffer %d is not a sensible ~%ds cushion for a remux", got, prebufferSeconds)
	}
}

// TestOrdinaryBitrateIsUnchanged: normal web releases must keep the existing
// behaviour, so this change cannot regress what already worked.
func TestOrdinaryBitrateIsUnchanged(t *testing.T) {
	s := bufferServer()
	// 4 GB over 2h ≈ 4.4 Mbps — 16 MiB is already ~30s of video.
	if got := s.playbackBufferBytes(bufferSession(4_000_000_000), torrent.DefaultBufferBytes); got != 0 {
		t.Fatalf("ordinary bitrate should keep the configured default (0 = unchanged), got %d", got)
	}
}

// TestBufferIsCapped keeps startup bounded on an extreme bitrate.
func TestBufferIsCapped(t *testing.T) {
	s := bufferServer()
	got := s.playbackBufferBytes(bufferSession(400_000_000_000), torrent.DefaultBufferBytes)
	if got > maxPrebufferBytes {
		t.Fatalf("buffer %d exceeds the cap %d", got, maxPrebufferBytes)
	}
}

// TestNeverBuffersMostOfASmallFile: a short high-bitrate clip must not have to
// download almost entirely before it can start.
func TestNeverBuffersMostOfASmallFile(t *testing.T) {
	s := bufferServer()
	const size = 100_000_000
	got := s.playbackBufferBytes(bufferSession(size), torrent.DefaultBufferBytes)
	if got > size/2 {
		t.Fatalf("buffer %d is more than half of a %d byte file", got, size)
	}
}

// TestUnknownSizeLeavesDefault: without a size there is nothing to scale from.
func TestUnknownSizeLeavesDefault(t *testing.T) {
	s := bufferServer()
	if got := s.playbackBufferBytes(bufferSession(0), torrent.DefaultBufferBytes); got != 0 {
		t.Fatalf("unknown size must leave the default in place, got %d", got)
	}
}
