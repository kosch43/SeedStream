package torrent

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestPlayheadPlacesTheViewerOnTheTimeline: a byte offset is only useful if it
// can be read as a position in the title.
func TestPlayheadPlacesTheViewerOnTheTimeline(t *testing.T) {
	const size = 6_000_000_000 // 6 GB
	const runtime = 7200       // 2 hours
	ph := NewPlayhead(size, runtime)

	// A quarter of the way through the file is half an hour into the film.
	ph.observeRead(size / 4)
	got := ph.Position()

	if got.ByteOffset != size/4 {
		t.Fatalf("byte offset %d, want %d", got.ByteOffset, size/4)
	}
	if got.PositionSeconds != 1800 {
		t.Fatalf("position %ds, want 1800 (30 minutes into a 2h title)", got.PositionSeconds)
	}
	if got.Percent < 24.9 || got.Percent > 25.1 {
		t.Fatalf("percent %.2f, want ~25", got.Percent)
	}
}

// TestPlayheadRunwayIsTimeNotBytes: the number that decides whether playback
// survives is how many seconds of video sit between the viewer and the first
// missing byte, which a byte count alone does not say.
func TestPlayheadRunwayIsTimeNotBytes(t *testing.T) {
	const size = 6_000_000_000
	const runtime = 7200 // ≈ 833 KB per second of video
	ph := NewPlayhead(size, runtime)

	ph.observeRead(1_000_000_000)
	ph.noteFrontier(1_050_000_000) // 50 MB ahead

	got := ph.Position()
	if got.RunwayBytes != 50_000_000 {
		t.Fatalf("runway %d bytes, want 50000000", got.RunwayBytes)
	}
	// 50 MB at 833 KB/s of video is about a minute.
	if got.RunwaySeconds < 55 || got.RunwaySeconds > 65 {
		t.Fatalf("runway %ds, want ~60", got.RunwaySeconds)
	}
}

// TestPlayheadRunwayIsZeroWhenTheDownloadIsBehind: a frontier at or behind the
// playhead means the next read has nothing to return. It must never come out
// negative or wrap into a large positive.
func TestPlayheadRunwayIsZeroWhenTheDownloadIsBehind(t *testing.T) {
	ph := NewPlayhead(1_000_000_000, 3600)
	ph.observeRead(500_000_000)
	ph.noteFrontier(400_000_000)

	if got := ph.Position(); got.RunwayBytes != 0 || got.RunwaySeconds != 0 {
		t.Fatalf("runway should be zero when the download is behind, got %d bytes / %ds",
			got.RunwayBytes, got.RunwaySeconds)
	}
}

// TestPlayheadWithoutRuntimeStillReportsBytes: runtime metadata is sometimes
// missing. The offset is still exact, so it must still be reported — only the
// seconds are unavailable, and they must not be invented.
func TestPlayheadWithoutRuntimeStillReportsBytes(t *testing.T) {
	ph := NewPlayhead(1_000_000_000, 0)
	ph.observeRead(250_000_000)

	got := ph.Position()
	if got.ByteOffset != 250_000_000 {
		t.Fatalf("byte offset %d, want 250000000", got.ByteOffset)
	}
	if got.Percent < 24.9 || got.Percent > 25.1 {
		t.Fatalf("percent %.2f, want ~25", got.Percent)
	}
	if got.PositionSeconds != 0 || got.RuntimeSeconds != 0 {
		t.Fatalf("without a runtime the timeline must be left blank, got %ds of %ds",
			got.PositionSeconds, got.RuntimeSeconds)
	}
}

// TestPlayheadCountsSeeksNotTheOpeningRequest: every stream opens with a seek
// (ServeContent probes the end for its Content-Length), so counting that would
// report a seek on every playback that never had one.
func TestPlayheadCountsSeeksNotTheOpeningRequest(t *testing.T) {
	ph := NewPlayhead(1_000_000_000, 3600)

	ph.observeSeek(999_999_999) // ServeContent sizing the file
	ph.observeSeek(0)           // back to the start
	if got := ph.Position().Seeks; got != 0 {
		t.Fatalf("seeks before any byte was served should not count, got %d", got)
	}

	ph.observeRead(1 << 20)
	ph.observeSeek(500_000_000) // the viewer drags the scrubber
	if got := ph.Position().Seeks; got != 1 {
		t.Fatalf("a seek during playback should count, got %d", got)
	}
}

// TestServingUpdatesThePosition is the wiring test: bytes going out through the
// same path playback uses must move the playhead, or none of the above is
// connected to anything.
func TestServingUpdatesThePosition(t *testing.T) {
	const size = 1 << 20
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	ph := NewPlayhead(size, 100) // 100s of video in 1 MiB
	r := newSeekableFileReader(fh, completeAvail(size), size, ph)
	defer r.Close()

	resp := serveRange(t, r, "bytes=0-524287") // the first half
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", resp.StatusCode)
	}

	got := ph.Position()
	if got.ByteOffset < size/2 {
		t.Fatalf("after serving half the file the playhead is at %d, want >= %d", got.ByteOffset, size/2)
	}
	if got.PositionSeconds < 45 || got.PositionSeconds > 55 {
		t.Fatalf("halfway through 100s of video should read ~50s, got %ds", got.PositionSeconds)
	}
}

// TestContiguousEndStopsAtTheFirstHole: the runway must be measured to the
// first missing piece, not to the end of everything downloaded. Reporting the
// latter would promise data that playback walks straight past.
func TestContiguousEndStopsAtTheFirstHole(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: 32, downloadedPieces: 10,
		lastPieceDone: true, // a downloaded tail, far beyond the hole at piece 10
		supportsPieces: true, fileSize: 32 * pieceSize,
	}
	a := newAvail(t, q)

	// From the start, the unbroken run is exactly the first 10 pieces — the
	// pre-fetched last piece is downloaded but unreachable from here.
	if got := a.ContiguousEnd(context.Background(), 0); got != 10*pieceSize {
		t.Fatalf("contiguous end %d, want %d (stop at the hole, not at the tail)", got, 10*pieceSize)
	}
}

// TestContiguousEndAtAHoleReportsNoRunway: sitting on a piece that has not
// arrived is zero runway, not the length of whatever follows it.
func TestContiguousEndAtAHoleReportsNoRunway(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: 32, downloadedPieces: 10,
		supportsPieces: true, fileSize: 32 * pieceSize,
	}
	a := newAvail(t, q)

	const at = 12 * pieceSize
	if got := a.ContiguousEnd(context.Background(), at); got != at {
		t.Fatalf("contiguous end %d, want %d — the playhead's own piece is missing", got, at)
	}
}

// TestContiguousEndOnACompleteFileIsFree: a finished torrent must not be
// re-queried on every measurement.
func TestContiguousEndOnACompleteFileIsFree(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: 8, downloadedPieces: 8,
		lastPieceDone: true, supportsPieces: true, fileSize: 8 * pieceSize,
	}
	a := newAvail(t, q)
	ctx := context.Background()

	if !a.BytesAvailable(ctx, 0, 4096) { // latch it complete
		t.Fatal("a fully downloaded file should be available")
	}
	before := atomic.LoadInt64(&q.pieceCalls)
	for i := 0; i < 50; i++ {
		if got := a.ContiguousEnd(ctx, 0); got != a.fileSize {
			t.Fatalf("a complete file has runway to the end, got %d", got)
		}
	}
	if got := atomic.LoadInt64(&q.pieceCalls) - before; got != 0 {
		t.Fatalf("a latched file must cost no requests, made %d", got)
	}
}
