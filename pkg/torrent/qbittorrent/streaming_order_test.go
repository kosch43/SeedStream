package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestEnsureStreamingOrderTurnsOnSequential(t *testing.T) {
	var calls []string
	state := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	c := New(Options{BaseURL: orderServer(t, &calls, state).URL, Category: "seedstream"})

	info := &TorrentInfo{Hash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd"}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 1 || calls[0] != "seq" {
		t.Fatalf("only sequential should be toggled on, got %v", calls)
	}
	if !info.SequentialDL {
		t.Fatal("sequential should be on")
	}
}

func TestEnsureStreamingOrderTurnsOffFirstLastPiecePrio(t *testing.T) {
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
	if len(calls) != 1 || calls[0] != "flpp" {
		t.Fatalf("only firstLastPiecePrio should be toggled off, got %v", calls)
	}
	if info.FirstLastPiecePrio {
		t.Fatal("firstLastPiecePrio should be off")
	}
}

func TestEnsureStreamingOrderAlreadyCorrect(t *testing.T) {
	var calls []string
	state := &TorrentInfo{
		Hash:         "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		SequentialDL: true,
	}
	c := New(Options{BaseURL: orderServer(t, &calls, state).URL, Category: "seedstream"})

	info := &TorrentInfo{
		Hash:         "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		SequentialDL: true,
	}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("correctly configured torrent must be left alone, got %v", calls)
	}
}

func TestEnsureStreamingOrderBothNeedChange(t *testing.T) {
	var calls []string
	state := &TorrentInfo{
		Hash:               "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		FirstLastPiecePrio: true,
	}
	c := New(Options{BaseURL: orderServer(t, &calls, state).URL, Category: "seedstream"})

	info := &TorrentInfo{
		Hash:               "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		FirstLastPiecePrio: true,
	}
	if err := c.EnsureStreamingOrder(context.Background(), info); err != nil {
		t.Fatalf("EnsureStreamingOrder: %v", err)
	}
	if len(calls) != 2 || calls[0] != "seq" || calls[1] != "flpp" {
		t.Fatalf("both flags need changing, got %v", calls)
	}
	if !info.SequentialDL {
		t.Fatal("sequential should be on")
	}
	if info.FirstLastPiecePrio {
		t.Fatal("firstLastPiecePrio should be off")
	}
}
