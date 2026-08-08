package torrent

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/torrent/qbittorrent"
)

// TestSeekIntoUnwrittenRegionBlocks is the end-to-end proof of the fix: with
// piece-level data, a read into a region that is on-disk-allocated but NOT
// downloaded must block (and here time out) rather than return a buffer of
// zeros. This is the exact scenario the old progress-math code got wrong.
func TestSeekIntoUnwrittenRegionBlocks(t *testing.T) {
	const pieceSize = 1 << 20
	const totalPieces = 32
	const fileSize = totalPieces * pieceSize
	const realPrefixPieces = 10

	// Build the on-disk file the way qBittorrent would leave it: real data in
	// the first 10 pieces plus the pre-fetched last piece, holes (sparse zeros)
	// in between.
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(fileSize); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	marker := bytes.Repeat([]byte{0xCD}, 4096)
	for off := int64(0); off < realPrefixPieces*pieceSize; off += int64(len(marker)) {
		f.WriteAt(marker, off)
	}
	f.WriteAt(marker, fileSize-int64(len(marker))) // last piece
	f.Close()

	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: totalPieces,
		downloadedPieces: realPrefixPieces, lastPieceDone: true,
		supportsPieces: true, fileSize: fileSize,
	}
	client := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})

	// Short timeout so the block is observable as a fast failure in the test.
	prev := seekWaitTimeout
	seekWaitTimeout = 2 * time.Second
	defer func() { seekWaitTimeout = prev }()

	avail := newFileAvailability(client, "cccccccccccccccccccccccccccccccccccccccc", 0, fileSize)
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r := newSeekableFileReader(fh, avail, fileSize, nil)
	defer r.Close()

	// Read from piece 5 (downloaded): must succeed with real data.
	r.Seek(5*pieceSize, 0)
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("piece 5 read failed: n=%d err=%v", n, err)
	}
	if buf[0] != 0xCD {
		t.Fatalf("piece 5 returned wrong data: %x", buf[0])
	}

	// Read from piece 11 (NOT downloaded): must block, then time out — it must
	// NOT return a zero buffer with nil error.
	r.Seek(11*pieceSize, 0)
	start := time.Now()
	n, err = r.Read(buf)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("read into unwritten region returned no error (n=%d) — served zeros instead of blocking", n)
	}
	if elapsed < time.Second {
		t.Fatalf("read returned too quickly (%v) — did not actually block on missing data", elapsed)
	}
}

// TestSeekBecomesReadableWhenPieceArrives: the block resolves once the piece
// downloads, so playback resumes rather than failing permanently.
func TestSeekBecomesReadableWhenPieceArrives(t *testing.T) {
	const pieceSize = 1 << 20
	const totalPieces = 16
	const fileSize = totalPieces * pieceSize

	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	f, _ := os.Create(path)
	f.Truncate(fileSize)
	marker := bytes.Repeat([]byte{0xEE}, 4096)
	f.WriteAt(marker, 8*pieceSize) // piece 8 has real data on disk already
	f.Close()

	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: totalPieces,
		downloadedPieces: 4, supportsPieces: true, fileSize: fileSize,
	}
	client := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})

	prev := seekWaitTimeout
	seekWaitTimeout = 10 * time.Second
	defer func() { seekWaitTimeout = prev }()

	avail := newFileAvailability(client, "dddddddddddddddddddddddddddddddddddddddd", 0, fileSize)
	fh, _ := os.Open(path)
	r := newSeekableFileReader(fh, avail, fileSize, nil)
	defer r.Close()

	r.Seek(8*pieceSize, 0)

	// Advance the download frontier shortly after the read starts blocking.
	done := make(chan struct{})
	go func() {
		time.Sleep(1500 * time.Millisecond)
		atomic.StoreInt64(&q.downloadedPieces, 12) // now covers piece 8
		close(done)
	}()

	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	<-done
	if err != nil || n == 0 {
		t.Fatalf("read should have unblocked once piece 8 downloaded: n=%d err=%v", n, err)
	}
	if buf[0] != 0xEE {
		t.Fatalf("unblocked read returned wrong data: %x", buf[0])
	}
}

func TestSeekWaitStopsWhenRequestIsCanceled(t *testing.T) {
	const pieceSize = 1 << 20
	const fileSize = 16 * pieceSize
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	if err := os.WriteFile(path, make([]byte, fileSize), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 16, supportsPieces: true, fileSize: fileSize}
	client := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})
	avail := newFileAvailability(client, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", 0, fileSize)
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()

	ctx, cancel := context.WithCancel(context.Background())
	r := newSeekableFileReaderWithContext(ctx, fh, avail, fileSize, nil)
	defer r.Close()
	r.Seek(8*pieceSize, io.SeekStart)

	result := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 4096))
		result <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("canceled read returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled read remained blocked")
	}
}
