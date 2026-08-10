package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"seedstream/pkg/torrent/qbittorrent"
)

// etaServer reports a torrent at a given progress and download rate.
func etaServer(t *testing.T, progress float64, dlSpeed int64) *qbittorrent.Client {
	t.Helper()
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%s","name":"T","size":%d,"progress":%v,"dlspeed":%d,"state":"downloading","category":"seedstream","save_path":"/d"}]`,
			hash, int64(7_100_000_000), progress, dlSpeed)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return qbittorrent.New(qbittorrent.Options{BaseURL: srv.URL, Category: "seedstream"})
}

// TestBudgetDoesNotExcuseOrderingEarly is the field failure, reproduced.
//
// A torrent 5% downloaded with ~33 seconds left took the deadline branch and
// was treated as "finishes in moments", which suppressed head ordering and made
// playback wait for the whole file. Thirty-three seconds is outside
// fastCompletionWindow, so only the budget test could have produced it — and
// the budget says nothing about where the pieces are.
func TestBudgetDoesNotExcuseOrderingEarly(t *testing.T) {
	// 5% of 7.1 GB left at a rate that finishes it in ~33s.
	remaining := int64(float64(7_100_000_000) * 0.95)
	c := etaServer(t, 0.05, remaining/33)

	deadline := time.Now().Add(78 * time.Second) // budget left, as in the logs
	fast, eta := nearingCompletion(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", deadline)

	if fast {
		t.Fatalf("at 5%% progress with %v to go, the prepare budget must not excuse piece ordering", eta)
	}
}

// TestBudgetStillAppliesNearTheEnd keeps the branch's real purpose. A torrent
// that genuinely is nearly done should not be reported as a piece-ordering
// fault just because the last pieces have not landed.
func TestBudgetStillAppliesNearTheEnd(t *testing.T) {
	remaining := int64(float64(7_100_000_000) * 0.03)
	c := etaServer(t, 0.97, remaining/40)

	deadline := time.Now().Add(78 * time.Second)
	fast, _ := nearingCompletion(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", deadline)

	if !fast {
		t.Fatal("at 97% with the remainder inside the budget, waiting for the file is the right call")
	}
}

// TestShortEtaNeedsRealProgressToo: the thirty-second window measures the
// download, not the request clock — but it is still not evidence about piece
// order. Measured in the field: a 5.6 GB file 48% downloaded at 125 MB/s had
// a 27-second ETA, took this branch, and playback waited for the whole file,
// because on a swarm that fast the in-flight window scatters pieces across
// the entire file and the continuous head completes only near the end. The
// window therefore carries the same progress gate as the budget branch: short
// ETA at the start of a download must keep the ordering pressure on.
func TestShortEtaNeedsRealProgressToo(t *testing.T) {
	remaining := int64(float64(7_100_000_000) * 0.95)
	c := etaServer(t, 0.05, remaining/5) // ~5 seconds left, at 5% progress

	fast, _ := nearingCompletion(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", time.Time{})
	if fast {
		t.Fatal("a five-second ETA at 5% progress must not excuse piece ordering; the pieces are scattered, not arriving")
	}
}

// TestShortEtaStillCountsNearTheEnd keeps the window's purpose once the
// download genuinely is at the end: there the remaining pieces land with the
// file, and reporting an ordering fault describes normal swarm behaviour.
func TestShortEtaStillCountsNearTheEnd(t *testing.T) {
	remaining := int64(float64(7_100_000_000) * 0.05)
	c := etaServer(t, 0.95, remaining/5) // ~5 seconds left, at 95% progress

	fast, _ := nearingCompletion(context.Background(), c, "abcdefabcdefabcdefabcdefabcdefabcdefabcd", time.Time{})
	if !fast {
		t.Fatal("a file five seconds from done at 95% progress is finishing in moments")
	}
}
