package torrent

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/torrent/qbittorrent"
)

const (
	seekPollInterval = 2 * time.Second
	seekWaitTimeout  = 5 * time.Minute
)

// SeekableFileReader wraps an *os.File belonging to an in-progress torrent.
// Before every Read it verifies — via qBittorrent's /torrents/files API —
// that the bytes at the current position are already on disk, blocking until
// they are or until seekWaitTimeout elapses.
//
// This makes forward seeks safe: instead of getting zeros from a
// pre-allocated-but-not-yet-written region, the player simply stalls for a
// moment while the sequential download catches up.
type SeekableFileReader struct {
	f         *os.File
	client    *qbittorrent.Client
	hash      string
	fileIndex int
	fileSize  int64
	pos       int64
}

func newSeekableFileReader(f *os.File, client *qbittorrent.Client, hash string, fileIndex int, fileSize int64) *SeekableFileReader {
	return &SeekableFileReader{
		f:         f,
		client:    client,
		hash:      hash,
		fileIndex: fileIndex,
		fileSize:  fileSize,
	}
}

// Read waits until the bytes at the current position are downloaded, then
// delegates to the underlying file.
func (r *SeekableFileReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.waitForBytes(r.pos, int64(len(p))); err != nil {
		return 0, err
	}
	n, err := r.f.Read(p)
	r.pos += int64(n)
	return n, err
}

// Seek delegates to the underlying file and keeps pos in sync.
// For io.SeekEnd it returns the logical file size (from torrent metadata)
// so http.ServeContent reports the correct Content-Length even when the
// physical file on disk is not yet fully written.
func (r *SeekableFileReader) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekEnd && r.fileSize > 0 {
		newPos := r.fileSize + offset
		if newPos < 0 {
			return 0, fmt.Errorf("seek before start of file")
		}
		// Seek the underlying file too so subsequent reads start correctly.
		if _, err := r.f.Seek(newPos, io.SeekStart); err != nil {
			return 0, err
		}
		r.pos = newPos
		return newPos, nil
	}
	newPos, err := r.f.Seek(offset, whence)
	if err == nil {
		r.pos = newPos
	}
	return newPos, err
}

// Close closes the underlying file.
func (r *SeekableFileReader) Close() error { return r.f.Close() }

// waitForBytes polls qBittorrent until at least (startByte + length) bytes of
// this file have been downloaded. Returns an error only on timeout or context
// cancellation — a fully-downloaded file returns immediately.
func (r *SeekableFileReader) waitForBytes(startByte, length int64) error {
	needed := startByte + length
	if needed >= r.fileSize {
		needed = r.fileSize
	}

	ctx, cancel := context.WithTimeout(context.Background(), seekWaitTimeout)
	defer cancel()

	for {
		files, err := r.client.Files(ctx, r.hash)
		if err == nil {
			for _, f := range files {
				if f.Index == r.fileIndex {
					downloaded := int64(f.Progress * float64(f.Size))
					if f.Progress >= 0.999 || downloaded >= needed {
						return nil
					}
					logger.Debug("SeekableFileReader: waiting for torrent bytes",
						"hash", r.hash[:min(8, len(r.hash))],
						"needed", needed,
						"downloaded", downloaded,
					)
					break
				}
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for torrent data at offset %d", startByte)
		case <-time.After(seekPollInterval):
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure *SeekableFileReader satisfies io.ReadSeekCloser.
var _ io.ReadSeekCloser = (*SeekableFileReader)(nil)
