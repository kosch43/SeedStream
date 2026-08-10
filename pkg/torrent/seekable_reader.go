package torrent

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"seedstream/pkg/core/logger"
)

// seekPollInterval is how long a read waits before re-asking whether its bytes
// have arrived. It is the granularity of every stall, so it is kept far below
// what a player will tolerate: a couple of seconds per miss compounds across one
// HTTP response into a client-side timeout and reconnect.
const seekPollInterval = 250 * time.Millisecond

// seekWaitTimeout bounds how long a Read blocks waiting for missing torrent
// data before failing. A var (not const) only so tests can shorten it.
var seekWaitTimeout = 5 * time.Minute

// gapSteerDelay is how long a read must be blocked on missing bytes before
// the download is steered at them. Shorter would steer on every ordinary
// piece-to-piece handoff; a healthy sequential download delivers the next
// piece well inside this window. Vars (not consts) only so tests can shorten
// them.
var gapSteerDelay = 3 * time.Second

// gapSteerInterval paces the steering calls while a read stays blocked. The
// call is cheap but the block can last minutes on a slow swarm, and issuing
// it every poll would be noise rather than pressure.
var gapSteerInterval = 10 * time.Second

// SeekableFileReader wraps an *os.File belonging to an in-progress torrent.
// Before every Read it verifies — through a fileAvailability checker — that
// the bytes at the current position are actually on disk, blocking until they
// are or until seekWaitTimeout elapses.
//
// With piece-level data from the download client the check is exact: a forward
// seek into a region no peer has delivered yet stalls until the pieces arrive,
// instead of serving zeros from an unwritten region. The checker also caches
// its answers, so steady-state playback costs at most one client round trip per
// cache window rather than one per 32 KiB read.
//
// On a seek, steerFunc (when set) is called with the new byte offset so the
// download client can re-anchor its sequential download at the viewer's new
// position, keeping the data the player needs ahead of the download order.
type SeekableFileReader struct {
	f        *os.File
	avail    *fileAvailability
	fileSize int64
	pos      int64
	ctxMu    sync.RWMutex
	ctx      context.Context

	// playhead, when set, is updated as bytes are served so the rest of
	// SeedStream can see where the viewer is. Nil is fine.
	playhead     *Playhead
	lastRunwayAt time.Time
	// warnedRunway suppresses repeat warnings for one continuous shortfall, so
	// a genuinely slow swarm produces one line rather than one per read.
	warnedRunway bool
	// steerFunc is called on seeks so the download can follow the viewer's
	// position. The function receives the new byte offset.
	steerFunc func(context.Context, int64)

	// gapSteerFunc is called when a read has been blocked on missing bytes for
	// long enough that the download is clearly not bringing them on its own.
	// It receives the blocked byte offset and points the client at the piece
	// that is actually missing, instead of waiting for the piece picker to get
	// around to it. Receives the same offset as steerFunc but is driven by a
	// stall rather than a seek.
	gapSteerFunc func(context.Context, int64)
	// gapBlockedSince is when the current continuous block on missing bytes
	// began; zero when the last read found its data.
	gapBlockedSince time.Time
	// lastGapSteer paces gap steering so a long stall issues the call on a
	// timer rather than on every poll.
	lastGapSteer time.Time
}

// runwayInterval is how often the contiguous run ahead of the playhead is
// re-measured. Reads arrive every few tens of kilobytes; the runway changes on
// the timescale of pieces, so measuring per read would be pure overhead.
const runwayInterval = 2 * time.Second

// lowRunwaySeconds is the point at which a stall is close enough to be worth
// saying out loud. Below this the player is within a few seconds of catching
// the download, which it will show as a freeze with no explanation.
const lowRunwaySeconds = 10

func newSeekableFileReader(f *os.File, avail *fileAvailability, fileSize int64, ph *Playhead) *SeekableFileReader {
	return newSeekableFileReaderWithContext(context.Background(), f, avail, fileSize, ph)
}

func newSeekableFileReaderWithContext(ctx context.Context, f *os.File, avail *fileAvailability, fileSize int64, ph *Playhead) *SeekableFileReader {
	if ctx == nil {
		ctx = context.Background()
	}
	return &SeekableFileReader{
		f:        f,
		avail:    avail,
		fileSize: fileSize,
		playhead: ph,
		ctx:      ctx,
	}
}

// SetContext binds reads to the request/session lifecycle. It is called before
// the reader is handed to http.ServeContent; a canceled context interrupts a
// wait for missing torrent pieces instead of leaving the connection blocked
// until seekWaitTimeout.
func (r *SeekableFileReader) SetContext(ctx context.Context) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.ctxMu.Lock()
	r.ctx = ctx
	r.ctxMu.Unlock()
}

func (r *SeekableFileReader) readContext() context.Context {
	if r == nil {
		return context.Background()
	}
	r.ctxMu.RLock()
	ctx := r.ctx
	r.ctxMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Playhead returns the position tracker, or nil when this reader has none.
func (r *SeekableFileReader) Playhead() *Playhead { return r.playhead }

// trackRunway re-measures how much unbroken data sits between the playhead and
// the first missing byte, on a timer so it costs nothing per read.
func (r *SeekableFileReader) trackRunway() {
	if r.playhead == nil || r.avail == nil {
		return
	}
	if time.Since(r.lastRunwayAt) < runwayInterval {
		return
	}
	r.lastRunwayAt = time.Now()

	ctx, cancel := context.WithTimeout(r.readContext(), 10*time.Second)
	defer cancel()
	end := r.avail.ContiguousEnd(ctx, r.pos)
	r.playhead.noteFrontier(end)

	pos := r.playhead.Position()
	// Only meaningful once the runtime is known; without it there is no way to
	// turn a byte count into "seconds before this stops".
	if pos.RuntimeSeconds <= 0 || end >= r.fileSize {
		r.warnedRunway = false
		return
	}
	if pos.RunwaySeconds < lowRunwaySeconds {
		if !r.warnedRunway {
			r.warnedRunway = true
			logger.Warn("Playback: the viewer is catching up with the download — a stall is likely",
				"position_seconds", pos.PositionSeconds, "runway_seconds", pos.RunwaySeconds,
				"runway_bytes", pos.RunwayBytes)
		}
		return
	}
	r.warnedRunway = false
}

// Read waits until the bytes at the current position are downloaded, then
// delegates to the underlying file.
//
// A short file on disk is never reported as EOF. Download clients write pieces
// as they arrive, so until the final piece lands the file is physically smaller
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
		select {
		case <-r.readContext().Done():
			return 0, r.readContext().Err()
		default:
		}
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
			r.playhead.observeRead(r.pos)
			r.trackRunway()
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
		timer := time.NewTimer(seekPollInterval)
		select {
		case <-r.readContext().Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return 0, r.readContext().Err()
		case <-timer.C:
		}
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
		r.playhead.observeSeek(newPos)
		return newPos, nil
	}
	newPos, err := r.f.Seek(offset, whence)
	if err == nil {
		r.pos = newPos
		r.playhead.observeSeek(newPos)
		// The runway ahead of the old position says nothing about the new one.
		r.lastRunwayAt = time.Time{}
		r.warnedRunway = false
	}
	if err == nil && r.steerFunc != nil {
		r.steerFunc(r.readContext(), newPos)
	}
	return newPos, err
}

// Close closes the underlying file.
func (r *SeekableFileReader) Close() error { return r.f.Close() }

// waitForBytes polls the availability checker until bytes
// [startByte, startByte+length) of the file are on disk. Returns an error
// only on timeout — an available region returns immediately, usually from
// the checker's cache without any network I/O.
//
// When the wait drags past gapSteerDelay the download is steered at the
// missing piece. Sequential order is a request order, not an arrival order:
// on a swarm delivering out of order, the piece the viewer is waiting on can
// sit behind scattered later pieces indefinitely, and the freeze only ends
// when the picker happens to circle back. Pointing the client at the exact
// missing piece makes it top priority, which is what turns a minutes-long
// stall into a moment's wait.
func (r *SeekableFileReader) waitForBytes(startByte, length int64) error {
	ctx, cancel := context.WithTimeout(r.readContext(), seekWaitTimeout)
	defer cancel()

	for {
		if r.avail.BytesAvailable(ctx, startByte, length) {
			r.gapBlockedSince = time.Time{}
			return nil
		}
		r.steerAtGap(ctx, startByte)
		select {
		case <-ctx.Done():
			if err := r.readContext().Err(); err != nil {
				return err
			}
			return fmt.Errorf("timeout waiting for torrent data at offset %d", startByte)
		case <-time.After(seekPollInterval):
		}
	}
}

// steerAtGap points the download at the piece the reader is blocked on, once
// the block has lasted long enough to be a stall rather than an ordinary
// piece handoff, and no more often than gapSteerInterval while it persists.
func (r *SeekableFileReader) steerAtGap(ctx context.Context, startByte int64) {
	if r.gapSteerFunc == nil {
		return
	}
	if r.gapBlockedSince.IsZero() {
		r.gapBlockedSince = time.Now()
		return
	}
	if time.Since(r.gapBlockedSince) < gapSteerDelay {
		return
	}
	if !r.lastGapSteer.IsZero() && time.Since(r.lastGapSteer) < gapSteerInterval {
		return
	}
	r.lastGapSteer = time.Now()
	r.gapSteerFunc(ctx, startByte)
}

// Ensure *SeekableFileReader satisfies io.ReadSeekCloser.
var _ io.ReadSeekCloser = (*SeekableFileReader)(nil)
