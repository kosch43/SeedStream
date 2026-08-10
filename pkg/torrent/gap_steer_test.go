package torrent

import (
	"context"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestFirstMissingPieceFindsTheHole is the shape every fragmented head and
// mid-stream freeze shares: a continuous run from byte zero, then a hole,
// then scattered pieces beyond. The answer must be the hole — the piece the
// sequential anchor has to move to — not the file's first piece, which is
// already on disk, and not an optimistic "all fine".
func TestFirstMissingPieceFindsTheHole(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 32, downloadedPieces: 10, lastPieceDone: true, supportsPieces: true, fileSize: 32 * pieceSize}
	a := newAvail(t, q)
	ctx := context.Background()

	// Prime the bitmap the way BytesAvailable would have seen it.
	if a.BytesAvailable(ctx, 0, pieceSize) != true {
		t.Fatal("the run from zero should be available")
	}

	// The head spans pieces 0..7; pieces 0..9 except the hole are... the mock
	// gives a contiguous 0..9, so ask past the run: the first missing piece at
	// or after piece 10's bytes is piece 10.
	piece, ok := a.FirstMissingPiece(ctx, 0, 16*pieceSize)
	if !ok || piece != 10 {
		t.Fatalf("first missing piece over [0,16MiB) = %d,%v; want 10,true", piece, ok)
	}

	// A range entirely inside the downloaded run has no hole.
	if _, ok := a.FirstMissingPiece(ctx, 0, 8*pieceSize); ok {
		t.Fatal("a fully-downloaded range has no missing piece")
	}

	// Starting inside the run still lands on the hole.
	if piece, ok := a.FirstMissingPiece(ctx, 5*pieceSize, 16*pieceSize); !ok || piece != 10 {
		t.Fatalf("first missing from 5MiB = %d,%v; want 10,true", piece, ok)
	}
}

// TestFirstMissingPieceAnswersNothingWhenItCannot: no piece data, or a latched
// complete file, means there is no hole to point at — and inventing one would
// steer a healthy download away from its order.
func TestFirstMissingPieceAnswersNothingWhenItCannot(t *testing.T) {
	const pieceSize = 1 << 20

	complete := completedAvailability(16 * pieceSize)
	if _, ok := complete.FirstMissingPiece(context.Background(), 0, pieceSize); ok {
		t.Fatal("a complete file has no missing pieces")
	}

	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 16, downloadedPieces: 4, supportsPieces: false, fileSize: 16 * pieceSize}
	fallback := newAvail(t, q)
	if _, ok := fallback.FirstMissingPiece(context.Background(), 0, pieceSize); ok {
		t.Fatal("without piece data there is no hole to name")
	}
}

// TestReaderSteersAtTheGapItIsBlockedOn: the freeze fix. A read sitting on a
// piece the swarm has not delivered must, after a short grace period, steer
// the download at that piece instead of waiting for the picker to circle back
// — which, measured in the field, took minutes while later pieces streamed
// past. And a read whose bytes are present must never steer: on a healthy
// sequential download the next piece arrives on its own, and steering at every
// handoff would be pure RPC churn.
func TestReaderSteersAtTheGapItIsBlockedOn(t *testing.T) {
	oldDelay, oldInterval := gapSteerDelay, gapSteerInterval
	gapSteerDelay, gapSteerInterval = 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { gapSteerDelay, gapSteerInterval = oldDelay, oldInterval })

	const pieceSize = 1 << 20
	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 16, downloadedPieces: 4, supportsPieces: true, fileSize: 16 * pieceSize}
	a := newAvail(t, q)

	f, err := os.CreateTemp(t.TempDir(), "gap-*.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16 * pieceSize); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := newSeekableFileReaderWithContext(ctx, f, a, 16*pieceSize, nil)

	var steers int64
	var steerOffset int64 = -1
	reader.gapSteerFunc = func(_ context.Context, off int64) {
		atomic.AddInt64(&steers, 1)
		atomic.StoreInt64(&steerOffset, off)
	}

	// A read whose bytes are already on disk: no steering, returns at once.
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32*1024)
	if _, err := reader.Read(buf); err != nil {
		t.Fatalf("read inside the run: %v", err)
	}
	if got := atomic.LoadInt64(&steers); got != 0 {
		t.Fatalf("an available read must not steer, got %d steers", got)
	}

	// A read at the gap (piece 4 is the hole): blocks, then steers at it.
	if _, err := reader.Seek(4*pieceSize, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := reader.Read(buf)
		done <- err
	}()

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt64(&steers) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&steers) == 0 {
		t.Fatal("a blocked read was never steered at its gap")
	}
	if off := atomic.LoadInt64(&steerOffset); off != 4*pieceSize {
		t.Fatalf("steered at offset %d, want the blocked offset %d", off, 4*pieceSize)
	}

	// The gap fills; the read completes instead of hanging.
	atomic.StoreInt64(&q.downloadedPieces, 8)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read after the gap filled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read did not resume once its piece arrived")
	}
}

// TestGapSteerSharedThrottleDeduplicatesAcrossReaders: the per-reader pacing in
// steerAtGap only gates one SeekableFileReader, and each player reconnect gets a
// fresh reader, so without the shared throttle N concurrent reads on the same
// stalled piece would fire N steers per interval. The shared map keyed on
// (client, hash, piece) collapses them to one.
func TestGapSteerSharedThrottleDeduplicatesAcrossReaders(t *testing.T) {
	// Drive the throttle directly: the production closure checks
	// gapSteerAllowed before SteerToPiece, so a second call for the same key
	// inside gapSteerInterval must be refused while a different piece is
	// still allowed.
	const key50 = "test\x00" + "abcdefabcdefabcdefabcdefabcdefabcdefabcd" + "\x00" + "50"
	const key200 = "test\x00" + "abcdefabcdefabcdefabcdefabcdefabcdefabcd" + "\x00" + "200"

	// Clean slate: neither piece has been steered.
	lastGapSteerTimes.Delete(key50)
	lastGapSteerTimes.Delete(key200)

	if !gapSteerAllowed(key50) {
		t.Fatal("an unsteered piece must be allowed through")
	}
	// Record a steer for piece 50, as the production closure does on success.
	lastGapSteerTimes.Store(key50, time.Now())

	if gapSteerAllowed(key50) {
		t.Fatal("a second steer for the same piece within gapSteerInterval must be refused")
	}
	// A different piece is a different hole and must not be suppressed.
	if !gapSteerAllowed(key200) {
		t.Fatal("a steer for a different piece must not be suppressed by an unrelated one")
	}

	// After the interval elapses, piece 50 is allowed again.
	lastGapSteerTimes.Store(key50, time.Now().Add(-gapSteerInterval-time.Second))
	if !gapSteerAllowed(key50) {
		t.Fatal("after gapSteerInterval elapses a steer must be allowed again")
	}

	lastGapSteerTimes.Delete(key50)
	lastGapSteerTimes.Delete(key200)
}

// TestGapSteerKeyIsPieceSpecific: the key includes the piece so a steer for one
// hole cannot suppress another. A key collision would make a stuck piece 200
// wait behind an unrelated piece 50's throttle.
func TestGapSteerKeyIsPieceSpecific(t *testing.T) {
	got := gapSteerKey("qb:type:https://x", "ABCDEF", 50)
	want := "qb:type:https://x\x00abcdef\x0050"
	if got != want {
		t.Fatalf("gapSteerKey = %q, want %q", got, want)
	}
	if gapSteerKey("c", "h", 50) == gapSteerKey("c", "h", 200) {
		t.Fatal("keys for different pieces must differ")
	}
	if gapSteerKey("c1", "h", 50) == gapSteerKey("c2", "h", 50) {
		t.Fatal("keys for different clients must differ")
	}
}
