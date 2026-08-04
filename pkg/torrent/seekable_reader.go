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
	seekWaitTimeout  = 60 * time.Second
)

// SeekableFileReader wraps an *os.File belonging to an in-progress torrent.
// Before every Read it verifies — via qBittorrent's piece states — that the
// bytes at the current position are already on disk, blocking until they are
// or until seekWaitTimeout elapses.
//
// This makes forward seeks safe: instead of getting zeros from a
// pre-allocated-but-not-yet-written region, the player simply stalls for a
// moment while the sequential download catches up.
//
// Availability is computed from per-piece download states rather than the
// torrent's aggregate download progress. qBittorrent is often configured with
// first/last-piece priority ("f_l_piece_prio"), which downloads the head AND
// the tail of a file early; a linear progress check would wrongly block
// reads of those pieces until the whole file completed.
type SeekableFileReader struct {
	f         *os.File
	client    *qbittorrent.Client
	hash      string
	fileIndex int
	fileSize  int64
	pos       int64

	// lazily populated metadata used to map file bytes to torrent pieces
	pieceSize int64
	fileStart int64 // byte offset of this file within the torrent
	loaded    bool
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

// loadMetadata fetches the torrent's piece size and the byte offset of this
// file within the torrent, so byte ranges can be mapped to piece indices. It
// only needs to run once.
func (r *SeekableFileReader) loadMetadata(ctx context.Context) error {
	files, err := r.client.Files(ctx, r.hash)
	if err != nil {
		return err
	}
	info, err := r.client.Get(ctx, r.hash)
	if err != nil {
		return err
	}
	if info != nil && info.PieceSize > 0 {
		r.pieceSize = info.PieceSize
	}
	var offset int64
	found := false
	for _, f := range files {
		if f.Index == r.fileIndex {
			r.fileStart = offset
			found = true
			break
		}
		offset += f.Size
	}
	if !found {
		return fmt.Errorf("file index %d not found in torrent", r.fileIndex)
	}
	r.loaded = true
	return nil
}

// waitForBytes polls qBittorrent until every piece covering the requested
// byte range of this file is downloaded. Returns an error only on timeout or
// context cancellation — a fully-downloaded file returns immediately.
func (r *SeekableFileReader) waitForBytes(startByte, length int64) error {
	if r.fileSize == 0 {
		return nil
	}
	endByte := startByte + length
	if endByte >= r.fileSize {
		endByte = r.fileSize - 1
	}
	if endByte < startByte {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), seekWaitTimeout)
	defer cancel()

	for {
		ready, err := r.bytesAvailable(ctx, startByte, endByte)
		if err == nil && ready {
			return nil
		}
		if err == nil {
			logger.Debug("SeekableFileReader: waiting for torrent bytes",
				"hash", r.hash[:min(8, len(r.hash))],
				"startByte", startByte,
				"endByte", endByte,
			)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for torrent data at offset %d", startByte)
		case <-time.After(seekPollInterval):
		}
	}
}

// bytesAvailable reports whether all pieces covering [startByte, endByte] of
// this file are fully downloaded (piece state 2).
func (r *SeekableFileReader) bytesAvailable(ctx context.Context, startByte, endByte int64) (bool, error) {
	if !r.loaded {
		if err := r.loadMetadata(ctx); err != nil {
			return false, err
		}
	}
	if r.pieceSize <= 0 {
		// Client didn't expose piece size — fall back to the old aggregate
		// progress heuristic so the worst case degrades to previous behavior.
		files, err := r.client.Files(ctx, r.hash)
		if err != nil {
			return false, err
		}
		for _, f := range files {
			if f.Index == r.fileIndex {
				downloaded := int64(f.Progress * float64(f.Size))
				if f.Progress >= 0.999 || downloaded >= endByte {
					return true, nil
				}
				return false, nil
			}
		}
		return false, fmt.Errorf("file index %d not found in torrent", r.fileIndex)
	}

	startPiece := (r.fileStart + startByte) / r.pieceSize
	endPiece := (r.fileStart + endByte) / r.pieceSize

	states, err := r.client.PieceStates(ctx, r.hash)
	if err != nil {
		return false, err
	}
	for i := startPiece; i <= endPiece && i < int64(len(states)); i++ {
		if states[i] < 2 {
			return false, nil
		}
	}
	return true, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure *SeekableFileReader satisfies io.ReadSeekCloser.
var _ io.ReadSeekCloser = (*SeekableFileReader)(nil)