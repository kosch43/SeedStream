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

// TestRemuxGetsAMuchLargerBuffer: at remux bitrates the fixed 16 MiB default is
// about two seconds of video, so playback starts and immediately outruns the
// download. The buffer must scale up with bitrate.
func TestRemuxGetsAMuchLargerBuffer(t *testing.T) {
	s := bufferServer()
	// 60 GB over a nominal 2h ≈ 67 Mbps — a 4K remux, 45s of cushion.
	got := s.playbackBufferBytes(bufferSession(60_000_000_000), 0)

	if got <= torrent.DefaultBufferBytes {
		t.Fatalf("a 60 GB remux must get more than the %d byte default, got %d",
			torrent.DefaultBufferBytes, got)
	}
	if got < 150_000_000 || got > torrent.MaxHeadBytes {
		t.Fatalf("buffer %d is not a sensible cushion for a remux", got)
	}
}

// TestOrdinary1080pStartsSooner is the complaint this scaling answers. A 1080p
// release is outpaced by any seedbox by a wide margin, so it needs seconds of
// cushion, not the 16 MiB the default imposed on everything. Waiting on data the
// stream was never going to need is time the viewer spends looking at a spinner.
func TestOrdinary1080pStartsSooner(t *testing.T) {
	s := bufferServer()
	// 4 GB over 2h ≈ 4.4 Mbps — an ordinary 1080p encode.
	got := s.playbackBufferBytes(bufferSession(4_000_000_000), 0)

	if got >= torrent.DefaultBufferBytes {
		t.Fatalf("an ordinary 1080p release should need less than the %d byte default, got %d",
			torrent.DefaultBufferBytes, got)
	}
	if got < torrent.MinHeadBytes {
		t.Fatalf("buffer %d is below the floor %d", got, torrent.MinHeadBytes)
	}
}

// TestBufferScalesMonotonicallyWithSize is the property the request asked for,
// stated directly: a bigger download never gets a smaller head than a smaller
// one. Checked across the whole ladder so a future edit to one rung cannot
// invert the ordering.
func TestBufferScalesMonotonicallyWithSize(t *testing.T) {
	s := bufferServer()
	sizes := []int64{
		700_000_000,     // 1080p x265 episode-sized
		2_000_000_000,   // 1080p WEB-DL
		8_000_000_000,   // 1080p Blu-ray
		25_000_000_000,  // 1080p remux
		60_000_000_000,  // 4K remux
		120_000_000_000, // oversized 4K remux
	}
	prev := int64(0)
	for _, size := range sizes {
		got := s.playbackBufferBytes(bufferSession(size), 0)
		if got < prev {
			t.Fatalf("a %d byte release got a %d byte head, less than the %d byte head of a smaller release",
				size, got, prev)
		}
		if got > torrent.MaxHeadBytes {
			t.Fatalf("size %d: buffer %d exceeds the cap %d", size, got, torrent.MaxHeadBytes)
		}
		prev = got
	}
}

// TestBufferIsCapped keeps startup bounded on an extreme bitrate.
func TestBufferIsCapped(t *testing.T) {
	s := bufferServer()
	got := s.playbackBufferBytes(bufferSession(400_000_000_000), 0)
	if got > torrent.MaxHeadBytes {
		t.Fatalf("buffer %d exceeds the cap %d", got, torrent.MaxHeadBytes)
	}
}

// TestNeverBuffersMostOfASmallFile: a short clip must not have to download
// almost entirely before it can start.
func TestNeverBuffersMostOfASmallFile(t *testing.T) {
	s := bufferServer()
	const size = 100_000_000
	got := s.playbackBufferBytes(bufferSession(size), 0)
	if got > size/2 {
		t.Fatalf("buffer %d is more than half of a %d byte file", got, size)
	}
}

// TestExplicitSettingIsAFloor: the built-in default must not hold small releases
// back, but an operator who typed a number meant it.
func TestExplicitSettingIsAFloor(t *testing.T) {
	s := bufferServer()
	const floor = 64 * 1024 * 1024
	got := s.playbackBufferBytes(bufferSession(4_000_000_000), floor)
	if got != floor {
		t.Fatalf("an explicitly configured buffer must be honoured, got %d want %d", got, floor)
	}
}

// TestUnknownSizeLeavesDefault: without a size there is nothing to scale from.
func TestUnknownSizeLeavesDefault(t *testing.T) {
	s := bufferServer()
	if got := s.playbackBufferBytes(bufferSession(0), 0); got != 0 {
		t.Fatalf("unknown size must leave the default in place, got %d", got)
	}
}
