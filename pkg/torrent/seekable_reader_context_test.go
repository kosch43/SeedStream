package torrent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSeekableFileReaderReadStopsWhenContextCanceled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(1 << 20); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	q := &pieceQBit{
		pieceSize:        1 << 20,
		totalPieces:      1,
		downloadedPieces: 0,
		supportsPieces:   true,
		fileSize:         1 << 20,
	}
	client := newAvail(t, q).client
	r := newSeekableFileReader(f, newFileAvailability(client, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 1<<20), 1<<20, nil)
	r.SetContext(ctx)
	defer r.Close()

	prev := seekWaitTimeout
	seekWaitTimeout = time.Minute
	defer func() { seekWaitTimeout = prev }()

	done := make(chan error, 1)
	go func() {
		_, readErr := r.Read(make([]byte, 4096))
		done <- readErr
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case readErr := <-done:
		if readErr == nil {
			t.Fatal("expected canceled read to return an error")
		}
	case <-time.After(time.Second):
		t.Fatal("read remained blocked after context cancellation")
	}
}
