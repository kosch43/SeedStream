package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/torrent/qbittorrent"
)

// etaQBit reports a torrent with a fixed progress, download rate and piece
// bitmap, so the fast-completion rule can be exercised against a head that is
// deliberately fragmented.
type etaQBit struct {
	pieceSize   int64
	totalPieces int
	downloaded  map[int]bool
	dlSpeed     int64
	infoCalls   int64
}

func (q *etaQBit) size() int64 { return q.pieceSize * int64(q.totalPieces) }

func (q *etaQBit) progress() float64 {
	n := 0
	for i := 0; i < q.totalPieces; i++ {
		if q.downloaded[i] {
			n++
		}
	}
	return float64(n) / float64(q.totalPieces)
}

func (q *etaQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.infoCalls, 1)
		fmt.Fprintf(w, `[{"hash":"%s","name":"Thing.S01E01.1080p","size":%d,"progress":%v,"dlspeed":%d,"state":"downloading","category":"seedstream","save_path":"/downloads","num_seeds":40,"num_complete":40}]`,
			hash, q.size(), q.progress(), q.dlSpeed)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"index":0,"name":"Thing.S01E01.1080p.mkv","size":%d,"progress":%v,"priority":1,"piece_range":[0,%d]}]`,
			q.size(), q.progress(), q.totalPieces-1)
	})
	mux.HandleFunc("/api/v2/torrents/properties", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"piece_size":%d,"pieces_num":%d,"total_size":%d}`, q.pieceSize, q.totalPieces, q.size())
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		states := make([]string, q.totalPieces)
		for i := range states {
			if q.downloaded[i] {
				states[i] = strconv.Itoa(qbittorrent.PieceDownloaded)
			} else {
				states[i] = "0"
			}
		}
		fmt.Fprint(w, "["+strings.Join(states, ",")+"]")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func etaManager(t *testing.T, q *etaQBit) *Manager {
	t.Helper()
	return NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})
}

// fragmentedFast builds the measured shape: most of the file downloaded, a hole
// early on, and a download rate that will finish the rest within seconds.
func fragmentedFast(dlSpeed int64) *etaQBit {
	q := &etaQBit{pieceSize: 1 << 20, totalPieces: 160, downloaded: map[int]bool{}, dlSpeed: dlSpeed}
	for i := 0; i < 160; i++ {
		q.downloaded[i] = i != 8 && i != 9 // continuous run of 8 pieces from the start
	}
	return q
}

// TestNearingCompletionUsesTheDownloadRate pins the arithmetic. 2 pieces of a
// 160-piece 1 MiB-per-piece torrent are missing, so ~2 MiB remains.
func TestNearingCompletionUsesTheDownloadRate(t *testing.T) {
	ctx := context.Background()

	// 1 MiB/s: about 2 seconds left — comfortably inside the window.
	q := fragmentedFast(1 << 20)
	c := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})
	fast, remain := nearingCompletion(ctx, c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", time.Time{})
	if !fast {
		t.Fatalf("2 MiB left at 1 MiB/s is seconds away, got not-fast (%v)", remain)
	}

	// 16 KiB/s: over two minutes left — this download is not about to finish.
	slow := fragmentedFast(16 << 10)
	cs := qbittorrent.New(qbittorrent.Options{BaseURL: slow.server(t).URL, Category: "seedstream"})
	if fast, remain := nearingCompletion(ctx, cs, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", time.Time{}); fast {
		t.Fatalf("2 MiB left at 16 KiB/s is %v away, which is not 'nearing completion'", remain)
	}
}

// TestNearingCompletionIsConservative: without a size or a rate there is nothing
// to compute, and the ordinary path must run rather than a guess.
func TestNearingCompletionIsConservative(t *testing.T) {
	q := fragmentedFast(0) // stalled: no rate to extrapolate from
	c := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})
	if fast, _ := nearingCompletion(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", time.Time{}); fast {
		t.Fatal("a stalled download must not be treated as about to finish")
	}
}

// TestFastFinishDoesNotWeakenTheHeadCheck is the regression that matters most.
// The rule changes how the wait is DESCRIBED, never what is served: a file with
// a hole inside the head must still not be handed to the player, however fast
// the download is going. Serving it would be the original bug returning.
func TestFastFinishDoesNotWeakenTheHeadCheck(t *testing.T) {
	q := fragmentedFast(1 << 20) // seconds from completion, head still holed
	mgr := etaManager(t, q)

	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		32<<20, PlaybackProfile{}, 4*time.Second, nil)
	if err == nil {
		t.Fatal("a head with a hole in it must not be served, however fast the download")
	}
	// And the reason given must not blame piece ordering, which is the point of
	// the rule: on a download this fast, ordering has had no chance to assert
	// itself and is not the problem.
	if contains(err.Error(), "out of order") {
		t.Fatalf("a download seconds from completion must not be reported as a piece-ordering fault, got: %v", err)
	}
}

// TestSlowFragmentedHeadIsStillReportedAsSuch: the opposite case must keep its
// diagnosis. Here ordering has had time to work and has not, which is a real
// finding an operator should see.
func TestSlowFragmentedHeadIsStillReportedAsSuch(t *testing.T) {
	q := fragmentedFast(16 << 10) // minutes from completion, head holed
	mgr := etaManager(t, q)

	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		32<<20, PlaybackProfile{}, 4*time.Second, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !contains(err.Error(), "out of order") {
		t.Fatalf("a slow download with a fragmented head is a genuine ordering problem, got: %v", err)
	}
}

// TestFastFinishStillServesTheMomentTheHeadIsWhole: the rule must not delay a
// stream that is genuinely ready. A continuous head is served immediately,
// whatever the download rate says about the rest of the file.
func TestFastFinishStillServesTheMomentTheHeadIsWhole(t *testing.T) {
	q := &etaQBit{pieceSize: 1 << 20, totalPieces: 160, downloaded: map[int]bool{}, dlSpeed: 1 << 20}
	for i := 0; i < 16; i++ { // a whole 16 MiB head and nothing else
		q.downloaded[i] = true
	}
	mgr := etaManager(t, q)

	start := time.Now()
	res, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		16<<20, PlaybackProfile{}, 20*time.Second, nil)
	if err != nil {
		t.Fatalf("a continuous head is playable and must be served at once: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %v for a head that was already whole", elapsed)
	}
	if res.Name != "Thing.S01E01.1080p.mkv" {
		t.Fatalf("unexpected file %q", res.Name)
	}
}

// TestHeadIsRevisedDownwardWhileWaiting is Fix 2. The head is first computed
// before the torrent exists, when no download rate can be observed, so it rests
// on a bitrate prior. The rate then ramps — 4 MB/s at t+4s, 112 MB/s at t+20s on
// a measured run — and deciding once at t=0 locks in the answer from the
// slowest moment.
//
// Here the head is asked for as 120 MiB but only 16 pieces are on disk. A rate
// far above playback must shrink the requirement enough for those 16 to satisfy
// it, so the stream starts instead of waiting for another 104 MiB it does not
// need.
func TestHeadIsRevisedDownwardWhileWaiting(t *testing.T) {
	q := &etaQBit{pieceSize: 1 << 20, totalPieces: 160, downloaded: map[int]bool{}, dlSpeed: 500 << 20}
	for i := 0; i < 16; i++ {
		q.downloaded[i] = true
	}
	mgr := etaManager(t, q)

	// 160 MiB over 600s is ~280 KB/s of playback; the download is ~1800x that.
	profile := PlaybackProfile{FileBytes: q.size(), RuntimeSeconds: 600}

	start := time.Now()
	res, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		120<<20, profile, 20*time.Second, nil)
	if err != nil {
		t.Fatalf("a download this far ahead of playback needs only a small head: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("waited %v — the requirement should have been revised on an early poll", elapsed)
	}
	if res.Name != "Thing.S01E01.1080p.mkv" {
		t.Fatalf("unexpected file %q", res.Name)
	}
}

// TestHeadRevisionNeverGrows: shrink only. Growing would move the goalposts
// mid-wait, so a momentary dip in a noisy rate would extend a wait already in
// progress against a fixed budget.
func TestHeadRevisionNeverGrows(t *testing.T) {
	q := &etaQBit{pieceSize: 1 << 20, totalPieces: 160, downloaded: map[int]bool{}, dlSpeed: 1000}
	mgr := etaManager(t, q)
	c := mgr.clients[0].client
	profile := PlaybackProfile{FileBytes: q.size(), RuntimeSeconds: 600}

	// A crawling download: the formula wants far more head than the current
	// requirement, and must not be allowed to raise it.
	warned := false
	const current = 8 << 20
	got := mgr.reviseHead(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		current, 120<<20, profile, 0, &warned)
	if got > current {
		t.Fatalf("the requirement grew from %d to %d mid-wait", current, got)
	}
}

// TestHeadRevisionWithoutAProfileIsANoOp: no size or runtime means no playback
// rate, so there is nothing to compute and the opening figure must stand.
func TestHeadRevisionWithoutAProfileIsANoOp(t *testing.T) {
	q := &etaQBit{pieceSize: 1 << 20, totalPieces: 160, downloaded: map[int]bool{}, dlSpeed: 500 << 20}
	mgr := etaManager(t, q)
	c := mgr.clients[0].client

	warned := false
	const current = 64 << 20
	got := mgr.reviseHead(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		current, current, PlaybackProfile{}, 0, &warned)
	if got != current {
		t.Fatalf("without a profile the head must be left alone, got %d want %d", got, current)
	}
}
