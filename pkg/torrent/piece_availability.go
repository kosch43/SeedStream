package torrent

import (
	"context"
	"sync"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/torrent/qbittorrent"
)

// availabilityCacheTTL is how long a fetched piece bitmap (or per-file
// progress, in fallback mode) is reused before asking qBittorrent again.
// During playback, reads arrive every ~32 KiB; without this cache each one
// would be a round trip to the seedbox, capping throughput at 32 KiB per RTT.
// Downloaded pieces never become un-downloaded, so a positive answer from a
// stale bitmap is still correct — the TTL only delays learning about NEW data.
const availabilityCacheTTL = 2 * time.Second

// fileAvailability answers "are bytes [offset, offset+length) of this file on
// disk?" for one file of one torrent.
//
// When the qBittorrent server provides piece-level data (piece_range on
// /torrents/files, qBittorrent >= 4.4, plus /torrents/pieceStates), the answer
// is exact: the per-piece bitmap says WHERE downloaded bytes are.
//
// Without it, nothing can be proven. Per-file progress is a count of downloaded
// bytes, not a description of where they sit, and torrents are added with
// first/last-piece priority so the file is sparse from the outset. Treating
// progress as a contiguous prefix is therefore wrong in exactly the region
// playback reaches after a few minutes, and being wrong is silent: a read into
// a sparse hole succeeds and yields zeros. Such a server can only serve a file
// once it is complete.
type fileAvailability struct {
	client    *qbittorrent.Client
	hash      string
	fileIndex int
	fileSize  int64

	mu       sync.Mutex
	initDone bool
	// piece mode (exact)
	pieceMode  bool
	pieceSize  int64
	firstPiece int
	lastPiece  int
	// aligned is true when the file begins exactly on a piece boundary, so a
	// file byte maps to a single, known piece. Single-file torrents and the
	// first file of any torrent are always aligned. When false the file starts
	// mid-piece by an unknown amount, so a byte could fall in either of two
	// pieces and the range check must widen by one to stay safe.
	aligned bool
	states  []int
	// fallback mode: no piece bitmap available. Nothing can be proven about
	// where downloaded bytes are, so only a completed file is servable.
	warnedNoPieces bool
	// shared
	lastFetch time.Time
	complete  bool // latched: every piece of the file confirmed downloaded
}

func newFileAvailability(client *qbittorrent.Client, hash string, fileIndex int, fileSize int64) *fileAvailability {
	return &fileAvailability{
		client:    client,
		hash:      hash,
		fileIndex: fileIndex,
		fileSize:  fileSize,
	}
}

// init discovers whether the server supports piece-level queries. Runs once;
// on any failure the checker degrades to the progress-based fallback rather
// than breaking playback on older qBittorrent versions.
// Caller must hold a.mu.
func (a *fileAvailability) initLocked(ctx context.Context) {
	if a.initDone {
		return
	}
	a.initDone = true

	files, err := a.client.Files(ctx, a.hash)
	if err != nil {
		logger.Debug("piece availability: files query failed, using progress fallback", "hash", shortHash(a.hash), "err", err)
		return
	}
	var pieceRange []int
	for _, f := range files {
		if f.Index == a.fileIndex {
			pieceRange = f.PieceRange
			break
		}
	}
	if len(pieceRange) < 2 || pieceRange[0] < 0 || pieceRange[1] < pieceRange[0] {
		logger.Debug("piece availability: no piece_range from server (qBittorrent < 4.4?), using progress fallback", "hash", shortHash(a.hash))
		return
	}
	props, err := a.client.Properties(ctx, a.hash)
	if err != nil || props == nil || props.PieceSize <= 0 {
		logger.Debug("piece availability: properties query failed, using progress fallback", "hash", shortHash(a.hash), "err", err)
		return
	}
	a.pieceSize = props.PieceSize
	a.firstPiece = pieceRange[0]
	a.lastPiece = pieceRange[1]
	a.pieceMode = true
	// A piece-aligned file spans exactly ceil(fileSize/pieceSize) pieces. If it
	// spans one more, its first byte sits mid-piece, so mapping is ambiguous by
	// one piece and the range check must widen.
	piecesIfAligned := int((a.fileSize + a.pieceSize - 1) / a.pieceSize)
	a.aligned = (a.lastPiece - a.firstPiece + 1) == piecesIfAligned
	logger.Debug("piece availability: exact piece tracking enabled",
		"hash", shortHash(a.hash), "piece_size", a.pieceSize,
		"first_piece", a.firstPiece, "last_piece", a.lastPiece, "aligned", a.aligned)
}

// BytesAvailable reports whether file bytes [offset, offset+length) are on
// disk. It never blocks beyond one qBittorrent round trip; callers that want
// to wait poll it (see SeekableFileReader.waitForBytes).
func (a *fileAvailability) BytesAvailable(ctx context.Context, offset, length int64) bool {
	if length <= 0 {
		return true
	}
	end := offset + length
	if end > a.fileSize {
		end = a.fileSize
	}
	if offset >= a.fileSize {
		return true // past EOF; the file read will return io.EOF, not garbage
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.complete {
		return true
	}
	a.initLocked(ctx)

	if a.pieceMode {
		return a.piecesAvailableLocked(ctx, offset, end)
	}
	return a.estimateAvailableLocked(ctx, end)
}

// piecesAvailableLocked checks the exact piece bitmap.
//
// For a piece-aligned file, file byte B is exactly at torrent piece
// firstPiece + B/pieceSize, so the range [offset, end) maps precisely. For an
// unaligned file the first byte sits mid-piece by an unknown amount, so byte B
// could fall in firstPiece + B/pieceSize or the next piece; the upper end is
// widened by one to cover both, which can only ever wait for one extra piece,
// never approve bytes that are not on disk.
// Caller must hold a.mu.
func (a *fileAvailability) piecesAvailableLocked(ctx context.Context, offset, end int64) bool {
	first := a.firstPiece + int(offset/a.pieceSize)
	last := a.firstPiece + int((end-1)/a.pieceSize)
	if !a.aligned {
		last++ // unknown intra-piece start: the range may reach one piece further
	}
	if last > a.lastPiece {
		last = a.lastPiece
	}
	if first > a.lastPiece {
		first = a.lastPiece
	}

	check := func() (ok, valid bool) {
		if len(a.states) <= a.lastPiece {
			return false, len(a.states) > 0
		}
		for i := first; i <= last; i++ {
			if a.states[i] != qbittorrent.PieceDownloaded {
				return false, true
			}
		}
		return true, true
	}

	// Downloaded pieces stay downloaded, so a cached "yes" is always safe.
	if ok, _ := check(); ok {
		a.latchCompleteLocked()
		return true
	}
	if time.Since(a.lastFetch) < availabilityCacheTTL {
		return false
	}

	states, err := a.client.PieceStates(ctx, a.hash)
	a.lastFetch = time.Now()
	if err != nil {
		logger.Debug("piece availability: pieceStates failed", "hash", shortHash(a.hash), "err", err)
		return false // transient failure: report unavailable, caller re-polls
	}
	if len(states) <= a.lastPiece {
		// Server returned a bitmap that does not cover the file's pieces —
		// inconsistent metadata. Degrade permanently to the progress fallback
		// rather than deadlocking on a bitmap that can never satisfy the check.
		logger.Warn("piece availability: pieceStates shorter than piece_range, degrading to progress fallback",
			"hash", shortHash(a.hash), "states", len(states), "last_piece", a.lastPiece)
		a.pieceMode = false
		return a.estimateAvailableLocked(ctx, end)
	}
	a.states = states

	ok, _ := check()
	if ok {
		a.latchCompleteLocked()
	}
	return ok
}

// latchCompleteLocked marks the checker permanently satisfied once every piece
// of the file is downloaded, so a finished file costs zero further API calls.
// Caller must hold a.mu.
func (a *fileAvailability) latchCompleteLocked() {
	if len(a.states) <= a.lastPiece {
		return
	}
	for i := a.firstPiece; i <= a.lastPiece; i++ {
		if a.states[i] != qbittorrent.PieceDownloaded {
			return
		}
	}
	a.complete = true
	a.states = nil // free the bitmap; it is no longer needed
}

// estimateAvailableLocked is the pre-piece-data behaviour: treat per-file
// progress as a contiguous prefix. Kept as the fallback for qBittorrent
// versions without piece_range. Caller must hold a.mu.
func (a *fileAvailability) estimateAvailableLocked(ctx context.Context, end int64) bool {
	if time.Since(a.lastFetch) < availabilityCacheTTL {
		return false // nothing has changed that could make an unproven range provable
	}
	files, err := a.client.Files(ctx, a.hash)
	a.lastFetch = time.Now()
	if err != nil {
		return false
	}
	for _, f := range files {
		if f.Index != a.fileIndex {
			continue
		}
		if f.Progress >= 1 {
			a.complete = true
			return true
		}
		// Incomplete, and without a piece bitmap there is no way to know where
		// the downloaded bytes are. Progress is a count, not a position, and
		// torrents are added with first/last-piece priority, so the file is
		// sparse from the start: head and tail present, a hole between them.
		// Treating progress as a contiguous prefix claims bytes that are not
		// there, and a read into a sparse hole succeeds and returns zeros, so
		// the player receives valid-looking nulls instead of video and drifts
		// out of sync with nothing logged. Refusing to answer makes playback
		// wait, which is visible and recoverable.
		if !a.warnedNoPieces {
			a.warnedNoPieces = true
			logger.Warn("piece-level data unavailable, so an incomplete torrent cannot be served safely; playback will wait until it completes",
				"hash", shortHash(a.hash), "progress", f.Progress)
		}
		return false
	}
	return false
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
