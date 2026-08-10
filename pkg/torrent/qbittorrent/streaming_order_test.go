package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestEnsureStreamingOrderSkipsDisabledFlags(t *testing.T) {
	var calls []string
	state := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	off := false
	c := New(Options{
		BaseURL:         orderServer(t, &calls, state).URL,
		Category:        "seedstream",
		SequentialOrder: &off,
		FirstLastFirst:  &off,
	})

	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("flags disabled in the client settings must not be enforced, got %v", calls)
	}
}

func TestEnsureStreamingOrderEnforcesOnlyWantedFlags(t *testing.T) {
	var calls []string
	state := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	off := false
	c := New(Options{
		BaseURL:         orderServer(t, &calls, state).URL,
		Category:        "seedstream",
		SequentialOrder: &off,
	})

	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 1 || calls[0] != "flpp" {
		t.Fatalf("only first/last-piece priority is wanted, got %v", calls)
	}
}

// TestEnsureStreamingOrderRaceCondition covers the race where a newly added
// torrent has its flags set by Add(), but Get() returns stale data showing
// them as off. The confirmation read must observe the settled add state and
// avoid toggling both flags back off.
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

	if len(calls) != 0 {
		t.Fatalf("settled add state was already correct; no toggle should be sent, got %v", calls)
	}
	if !info.SequentialDL || !info.FirstLastPiecePrio {
		t.Fatal("info should reflect the verified state (both flags on)")
	}
}

func TestEnsureStreamingOrderSerializesConcurrentClients(t *testing.T) {
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	var mu sync.Mutex
	state := TorrentInfo{Hash: hash}
	var calls []string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, `[{"hash":%q,"seq_dl":%t,"f_l_piece_prio":%t}]`,
			state.Hash, state.SequentialDL, state.FirstLastPiecePrio)
	})
	mux.HandleFunc("/api/v2/torrents/toggleSequentialDownload", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, "seq")
		state.SequentialDL = !state.SequentialDL
	})
	mux.HandleFunc("/api/v2/torrents/toggleFirstLastPiecePrio", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, "flpp")
		state.FirstLastPiecePrio = !state.FirstLastPiecePrio
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	clients := []*Client{
		New(Options{BaseURL: srv.URL}),
		New(Options{BaseURL: srv.URL}),
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, c := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			errs <- c.EnsureStreamingOrder(context.Background(), &TorrentInfo{Hash: hash})
		}(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("EnsureStreamingOrder: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "seq" || calls[1] != "flpp" {
		t.Fatalf("concurrent callers must toggle each flag exactly once, got %v", calls)
	}
	if !state.SequentialDL || !state.FirstLastPiecePrio {
		t.Fatalf("streaming flags ended disabled: %+v", state)
	}
}
