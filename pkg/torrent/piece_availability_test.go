package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/torrent/qbittorrent"
)

// pieceQBit is a mock qBittorrent for exercising fileAvailability. It serves a
// single-file torrent whose pieces are downloaded contiguously up to
// downloadedPieces, PLUS the final piece (mimicking firstLastPiecePrio). Set
// supportsPieces=false to emulate an old server that omits piece_range and
// pieceStates, forcing the progress fallback.
type pieceQBit struct {
	pieceSize        int64
	totalPieces      int
	downloadedPieces int64 // atomic; contiguous prefix in pieces
	lastPieceDone    bool
	supportsPieces   bool
	fileSize         int64
	filesCalls       int64
	pieceCalls       int64
	propsCalls       int64
}

func (q *pieceQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.filesCalls, 1)
		dp := atomic.LoadInt64(&q.downloadedPieces)
		prog := float64(dp) / float64(q.totalPieces)
		if q.lastPieceDone {
			prog += 1.0 / float64(q.totalPieces)
		}
		if prog > 1 {
			prog = 1
		}
		if q.supportsPieces {
			fmt.Fprintf(w, `[{"index":0,"name":"video.mkv","size":%d,"progress":%v,"priority":1,"piece_range":[0,%d]}]`,
				q.fileSize, prog, q.totalPieces-1)
		} else {
			fmt.Fprintf(w, `[{"index":0,"name":"video.mkv","size":%d,"progress":%v,"priority":1}]`, q.fileSize, prog)
		}
	})
	mux.HandleFunc("/api/v2/torrents/properties", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.propsCalls, 1)
		if !q.supportsPieces {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"piece_size":%d,"pieces_num":%d,"total_size":%d}`, q.pieceSize, q.totalPieces, q.fileSize)
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.pieceCalls, 1)
		if !q.supportsPieces {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		dp := int(atomic.LoadInt64(&q.downloadedPieces))
		states := make([]string, q.totalPieces)
		for i := 0; i < q.totalPieces; i++ {
			if i < dp {
				states[i] = "2"
			} else {
				states[i] = "0"
			}
		}
		if q.lastPieceDone {
			states[q.totalPieces-1] = "2"
		}
		w.Write([]byte("[" + join(states, ",") + "]"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func join(s []string, sep string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}

func newAvail(t *testing.T, q *pieceQBit) *fileAvailability {
	t.Helper()
	srv := q.server(t)
	client := qbittorrent.New(qbittorrent.Options{BaseURL: srv.URL, Category: "seedstream"})
	return newFileAvailability(client, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, q.fileSize)
}

// TestPieceAvailabilityRejectsUnwrittenRegion is the core regression: the exact
// bug from the audit. Contiguous prefix is 10 pieces, plus the last piece
// pre-fetched. Progress claims 11 pieces done. A read at piece 10 (inside the
// overstated window) must be reported UNAVAILABLE, where the old progress math
// wrongly approved it.
func TestPieceAvailabilityRejectsUnwrittenRegion(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{
		pieceSize:        pieceSize,
		totalPieces:      32,
		downloadedPieces: 10,
		lastPieceDone:    true, // firstLastPiecePrio
		supportsPieces:   true,
		fileSize:         32 * pieceSize,
	}
	a := newAvail(t, q)
	ctx := context.Background()

	// Byte inside piece 5 — genuinely downloaded.
	if !a.BytesAvailable(ctx, 5*pieceSize, 4096) {
		t.Fatalf("piece 5 should be available")
	}
	// Byte inside piece 10 — NOT downloaded, but old progress math (11/32)
	// would have approved it. This is the bug.
	if a.BytesAvailable(ctx, 10*pieceSize+4096, 4096) {
		t.Fatalf("piece 10 is not downloaded; must be reported unavailable")
	}
	// The pre-fetched last piece IS downloaded and should be available.
	if !a.BytesAvailable(ctx, int64(q.totalPieces-1)*pieceSize, 4096) {
		t.Fatalf("last piece was pre-fetched; should be available")
	}
}

// TestPieceAvailabilityBecomesAvailable checks that once the missing piece
// downloads, the checker approves it (after the cache window forces a refetch).
func TestPieceAvailabilityBecomesAvailable(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 16, downloadedPieces: 4, supportsPieces: true, fileSize: 16 * pieceSize}
	a := newAvail(t, q)
	ctx := context.Background()

	if a.BytesAvailable(ctx, 8*pieceSize, 4096) {
		t.Fatalf("piece 8 not downloaded yet")
	}
	// Simulate the download frontier advancing past piece 8.
	atomic.StoreInt64(&q.downloadedPieces, 12)
	a.lastFetch = a.lastFetch.Add(-availabilityCacheTTL * 2) // force refetch
	if !a.BytesAvailable(ctx, 8*pieceSize, 4096) {
		t.Fatalf("piece 8 downloaded now; should be available")
	}
}

// TestPieceAvailabilityCaches confirms repeated reads in the cache window do
// not hammer qBittorrent — the throughput fix.
func TestPieceAvailabilityCaches(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 64, downloadedPieces: 64, lastPieceDone: true, supportsPieces: true, fileSize: 64 * pieceSize}
	a := newAvail(t, q)
	ctx := context.Background()

	for i := 0; i < 500; i++ {
		if !a.BytesAvailable(ctx, int64(i%32)*pieceSize, 4096) {
			t.Fatalf("fully downloaded torrent: read %d should be available", i)
		}
	}
	// Fully complete → latched after first confirming pass; pieceStates should
	// have been fetched at most a handful of times, never 500.
	if pc := atomic.LoadInt64(&q.pieceCalls); pc > 3 {
		t.Fatalf("expected pieceStates to be cached/latched, got %d calls for 500 reads", pc)
	}
}

// TestPieceAvailabilityFallback checks an old qBittorrent (no piece_range,
// pieceStates 404s) degrades to progress-based availability without erroring.
func TestPieceAvailabilityFallback(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 20, downloadedPieces: 10, supportsPieces: false, fileSize: 20 * pieceSize}
	a := newAvail(t, q)
	ctx := context.Background()

	// Progress is around 0.5, but it is a count of downloaded bytes rather than
	// a description of where they are, and first/last-piece priority makes the
	// file sparse from the start. Without a piece bitmap no specific range can
	// be proven present, so an incomplete file must be refused everywhere —
	// including near the beginning, which the old contiguous-prefix assumption
	// wrongly allowed and which silently served zeros from a sparse hole.
	if a.BytesAvailable(ctx, 2*pieceSize, 4096) {
		t.Fatalf("fallback must not claim early bytes it cannot prove are present")
	}
	if a.BytesAvailable(ctx, 15*pieceSize, 4096) {
		t.Fatalf("fallback must not claim later bytes either")
	}
	if a.pieceMode {
		t.Fatalf("should have fallen back to estimate mode, not piece mode")
	}
	if atomic.LoadInt64(&q.pieceCalls) != 0 {
		t.Fatalf("fallback must not call pieceStates")
	}
}

// TestPieceAvailabilityPastEOF: reads past the end of the file return available
// (the file read itself yields io.EOF; the checker must not block forever).
func TestPieceAvailabilityPastEOF(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{pieceSize: pieceSize, totalPieces: 8, downloadedPieces: 2, supportsPieces: true, fileSize: 8 * pieceSize}
	a := newAvail(t, q)
	if !a.BytesAvailable(context.Background(), 8*pieceSize+1, 4096) {
		t.Fatalf("reads past EOF must return available, not block")
	}
}

// TestStaleNegativeIsRefreshedNotTrusted is the regression test for the stall
// cadence. A cached "yes" is safe because pieces never un-download, but a cached
// "no" only means "not in my last snapshot" — and trusting it sent the reader to
// sleep for a whole poll interval over bytes already on disk. Stacked across one
// HTTP response those add up to a client timeout and reconnect.
func TestStaleNegativeIsRefreshedNotTrusted(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: 20, downloadedPieces: 4,
		supportsPieces: true, fileSize: 20 * pieceSize,
	}
	a := newAvail(t, q)
	ctx := context.Background()

	// Prime the bitmap: piece 10 is not yet downloaded.
	if a.BytesAvailable(ctx, 10*pieceSize, 4096) {
		t.Fatal("precondition: piece 10 should not be available yet")
	}
	callsAfterPrime := atomic.LoadInt64(&q.pieceCalls)

	// The piece lands. The cached bitmap is now stale but still young.
	atomic.StoreInt64(&q.downloadedPieces, 15)

	// Must re-fetch rather than trusting the stale negative. Sleeping first would
	// be the old behaviour; the point is that no wait is needed.
	time.Sleep(negativeRecheckInterval + 50*time.Millisecond)
	if !a.BytesAvailable(ctx, 10*pieceSize, 4096) {
		t.Fatal("a negative must be re-checked against the client, not served from cache")
	}
	if atomic.LoadInt64(&q.pieceCalls) <= callsAfterPrime {
		t.Fatal("expected a refresh request after the miss")
	}
}

// TestPositiveStillServedFromCache: the happy path must not start making a
// request per read, which is what the cache exists to prevent.
func TestPositiveStillServedFromCache(t *testing.T) {
	const pieceSize = 1 << 20
	q := &pieceQBit{
		pieceSize: pieceSize, totalPieces: 20, downloadedPieces: 20,
		supportsPieces: true, fileSize: 20 * pieceSize,
	}
	a := newAvail(t, q)
	ctx := context.Background()

	if !a.BytesAvailable(ctx, 0, 4096) {
		t.Fatal("precondition: should be available")
	}
	before := atomic.LoadInt64(&q.pieceCalls)
	for i := 0; i < 200; i++ {
		if !a.BytesAvailable(ctx, int64(i)*4096, 4096) {
			t.Fatalf("read %d should be available", i)
		}
	}
	if got := atomic.LoadInt64(&q.pieceCalls) - before; got != 0 {
		t.Fatalf("cached positives must cost no requests, made %d", got)
	}
}
