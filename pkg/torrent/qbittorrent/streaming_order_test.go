package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// orderServer records which toggle endpoints were called.
func orderServer(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/toggleSequentialDownload", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "seq")
	})
	mux.HandleFunc("/api/v2/torrents/toggleFirstLastPiecePrio", func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, "flpp")
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
	c := New(Options{BaseURL: orderServer(t, &calls).URL, Category: "seedstream"})

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
	c := New(Options{BaseURL: orderServer(t, &calls).URL, Category: "seedstream"})

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
	c := New(Options{BaseURL: orderServer(t, &calls).URL, Category: "seedstream"})

	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd", SequentialDL: true}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 1 || calls[0] != "flpp" {
		t.Fatalf("only first/last-piece priority was missing, got %v", calls)
	}
}
