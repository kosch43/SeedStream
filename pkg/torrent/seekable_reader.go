package torrent

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const seekPollInterval = 2 * time.Second

// seekWaitTimeout bounds how long a Read blocks waiting for missing torrent
// data before failing. A var (not const) only so tests can shorten it.
var seekWaitTimeout = 5 * time.Minute

// SeekableFileReader wraps an *os.File belonging to an in-progress torrent.
// Before every Read it verifies — through a fileAvailability checker — that
// the bytes at the current position are actually on disk, blocking until they
// are or until seekWaitTimeout elapses.
//
// With piece-level data from qBittorrent the check is exact: a forward seek
// into a region no peer has delivered yet stalls until the pieces arrive,
// instead of serving zeros from an unwritten region. The checker also caches
// its answers, so steady-state playback costs at most one qBittorrent round
// trip per cache window rather than one per 32 KiB read.
type SeekableFileReader struct {
	f        *os.File
	avail    *fileAvailability
	fileSize int64
	pos      int64
}

func newSeekableFileReader(f *os.File, avail *fileAvailability, fileSize int64) *SeekableFileReader {
	return &SeekableFileReader{
		f:        f,
		avail:    avail,
		fileSize: fileSize,
	}
}

// Read waits until the bytes at the current position are downloaded, then
// delegates to the underlying file.
//
// A short file on disk is never reported as EOF. qBittorrent writes pieces as
// they arrive, so until the final piece lands the file is physically smaller
// than the torrent says it will be. A read past that point returns io.EOF from
// the OS even though the bytes are still coming, and http.ServeContent has
// already promised the full length in Content-Length — so returning EOF ends the
// response early and the player treats a seek near the end as the end of the
// video. Only the logical size decides when the stream is over.
func (r *SeekableFileReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.fileSize > 0 && r.pos >= r.fileSize {
		return 0, io.EOF // genuine end of the stream
	}
	deadline := time.Now().Add(seekWaitTimeout)
	for {
		if err := r.waitForBytes(r.pos, int64(len(p))); err != nil {
			return 0, err
		}
		// Positional read. r.f.Read would take the descriptor's own offset,
		// which is only equal to r.pos while every prior operation has kept the
		// two in step — and a partial read or a re-seek during recovery can
		// break that. Reading at an offset a few bytes from the one just
		// verified lands mid-frame, which the player shows as a jump. ReadAt
		// neither uses nor moves the descriptor offset, so the bytes returned
		// are always the bytes that were checked.
		n, err := r.f.ReadAt(p, r.pos)
		if n > 0 {
			r.pos += int64(n)
			if err == io.EOF && r.pos < r.fileSize {
				err = nil // more is coming; do not end the response here
			}
			return n, err
		}
		if err != nil && err != io.EOF {
			return 0, err
		}
		// Zero bytes at a position the torrent says exists: the tail has not
		// been written to disk yet. Wait for it rather than truncating.
		if r.fileSize > 0 && r.pos >= r.fileSize {
			return 0, io.EOF
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout waiting for torrent data at offset %d of %d", r.pos, r.fileSize)
		}
		time.Sleep(seekPollInterval)
	}
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

// waitForBytes polls the availability checker until bytes
// [startByte, startByte+length) of the file are on disk. Returns an error
// only on timeout — an available region returns immediately, usually from
// the checker's cache without any network I/O.
func (r *SeekableFileReader) waitForBytes(startByte, length int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), seekWaitTimeout)
	defer cancel()

	for {
		if r.avail.BytesAvailable(ctx, startByte, length) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for torrent data at offset %d", startByte)
		case <-time.After(seekPollInterval):
		}
	}
}

// Ensure *SeekableFileReader satisfies io.ReadSeekCloser.
var _ io.ReadSeekCloser = (*SeekableFileReader)(nil)
