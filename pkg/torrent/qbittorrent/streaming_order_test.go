package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// orderServer records which toggle endpoints were called.
// state is the current state of the torrent as reported by Get().
func orderServer(t *testing.T, calls *[]string, state *TorrentInfo) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		if state == nil {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"hash":%q,"seq_dl":%t,"f_l_piece_prio":%t}]`,
			state.Hash, state.SequentialDL, state.FirstLastPiecePrio)
	})
	mux.HandleFunc("/api/v2/torrents/toggleSequentialDownload", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "seq")
		if state != nil {
			state.SequentialDL = !state.SequentialDL
		}
	})
	mux.HandleFunc("/api/v2/torrents/toggleFirstLastPiecePrio", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "flpp")
		if state != nil {
			state.FirstLastPiecePrio = !state.FirstLastPiecePrio
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestEnsureStreamingOrderTurnsOnWhatIsMissing covers the torrent qBittorrent
// already held: the add that "created" it was a no-op, so the streaming flags
// in that request were discarded and it has been downloading rarest-first.
func TestEnsureStreamingOrderTurnsOnWhatIsMissing(t *testing.T) {
	var calls []string
	state := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	c := New(Options{BaseURL: orderServer(t, &calls, state).URL, Category: "seedstream"})

	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 2 || calls[0] != "seq" || calls[1] != "flpp" {
		t.Fatalf("both flags were off and both should have been switched on, got %v", calls)
	}
	if !info.SequentialDL || !info.FirstLastPiecePrio {
		t.Fatal("the passed-in info should reflect the new state")
	}
}

// TestEnsureStreamingOrderIsNotAToggle is the hazard worth a test: both
// endpoints flip the flag rather than set it, so calling them on a torrent that
// is already configured correctly would switch sequential download back OFF and
// leave the very fragmentation this is meant to prevent.
func TestEnsureStreamingOrderIsNotAToggle(t *testing.T) {
	var calls []string
	state := &TorrentInfo{
		Hash:               "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		SequentialDL:       true,
		FirstLastPiecePrio: true,
	}
	c := New(Options{BaseURL: orderServer(t, &calls, state).URL, Category: "seedstream"})

	info := &TorrentInfo{
		Hash:               "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		SequentialDL:       true,
		FirstLastPiecePrio: true,
	}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("a correctly configured torrent must be left alone, got %v", calls)
	}
}

// TestEnsureStreamingOrderPartial: only the missing flag is touched.
func TestEnsureStreamingOrderPartial(t *testing.T) {
	var calls []string
	state := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", SequentialDL: true}
	c := New(Options{BaseURL: orderServer(t, &calls, state).URL, Category: "seedstream"})

	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", SequentialDL: true}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 1 || calls[0] != "flpp" {
		t.Fatalf("only first/last-piece priority was missing, got %v", calls)
	}
}

// TestEnsureStreamingOrderRaceCondition covers the race where a newly added
// torrent has its flags set by Add(), but Get() returns stale data showing
// them as off. The toggle would flip them off after qBittorrent processes
// the add. The fix re-reads state before toggling and verifies after.
func TestEnsureStreamingOrderRaceCondition(t *testing.T) {
	var calls []string
	// Simulate qBittorrent processing the add after the first Get() call
	getCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		getCount++
		// First Get() returns stale data (flags off)
		// Subsequent Get() calls return correct data (flags on, set by the add)
		if getCount == 1 {
			fmt.Fprintf(w, `[{"hash":"abcdefabcdefabcdefabcdefabcdefabcdefabcd","seq_dl":false,"f_l_piece_prio":false}]`)
		} else {
			fmt.Fprintf(w, `[{"hash":"abcdefabcdefabcdefabcdefabcdefabcdefabcd","seq_dl":true,"f_l_piece_prio":true}]`)
		}
	})
	mux.HandleFunc("/api/v2/torrents/toggleSequentialDownload", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "seq")
	})
	mux.HandleFunc("/api/v2/torrents/toggleFirstLastPiecePrio", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "flpp")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, Category: "seedstream"})
	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}

	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}

	// The first Get() showed flags off, so toggles were called
	// The second Get() (verification) showed flags on, so no more toggles
	if len(calls) != 2 || calls[0] != "seq" || calls[1] != "flpp" {
		t.Fatalf("expected exactly one toggle of each flag, got %v", calls)
	}
	if !info.SequentialDL || !info.FirstLastPiecePrio {
		t.Fatal("info should reflect the verified state (both flags on)")
	}
}
