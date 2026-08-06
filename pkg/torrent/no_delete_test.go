package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"seedstream/pkg/core/config"
)

// hnrQBit records whether anything destructive was ever asked of the client.
type hnrQBit struct {
	deleteCalls     int64
	addCalls        int64
	shareLimitCalls int64
	lastShareForm   atomic.Value // string
}

func (q *hnrQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.deleteCalls, 1)
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.addCalls, 1)
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/setShareLimits", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&q.shareLimitCalls, 1)
		_ = r.ParseForm()
		q.lastShareForm.Store(r.Form.Encode())
		fmt.Fprint(w, "Ok.")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func hnrManager(t *testing.T, q *hnrQBit) *Manager {
	t.Helper()
	return NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})
}

// TestReplaceNeverDeletes is the hit-and-run guarantee: swapping in a healthier
// torrent must add alongside the old one and never remove anything, because a
// removal is the one action that cannot be undone if the tracker's accounting
// disagrees with qBittorrent's.
func TestReplaceNeverDeletes(t *testing.T) {
	q := &hnrQBit{}
	mgr := hnrManager(t, q)

	err := mgr.Replace(context.Background(), "box",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := atomic.LoadInt64(&q.deleteCalls); got != 0 {
		t.Fatalf("replacement must never delete a torrent, got %d delete call(s)", got)
	}
	if got := atomic.LoadInt64(&q.addCalls); got != 1 {
		t.Fatalf("replacement should add the alternative once, got %d", got)
	}
}

// TestProtectSeedingPinsLimits: qBittorrent's global ratio and seed-time limits
// can stop seeding on their own, which would cut a hit-and-run obligation short
// without SeedStream doing anything. Torrents must be pinned to no limit.
func TestProtectSeedingPinsLimits(t *testing.T) {
	q := &hnrQBit{}
	mgr := hnrManager(t, q)

	mgr.ProtectSeeding(context.Background(), "box", "cccccccccccccccccccccccccccccccccccccccc")

	if got := atomic.LoadInt64(&q.shareLimitCalls); got != 1 {
		t.Fatalf("expected one setShareLimits call, got %d", got)
	}
	form, _ := q.lastShareForm.Load().(string)
	for _, want := range []string{"ratioLimit=-1", "seedingTimeLimit=-1", "inactiveSeedingTimeLimit=-1"} {
		if !contains(form, want) {
			t.Errorf("share limits should disable %s, form was %q", want, form)
		}
	}
}

// TestProtectSeedingIsBestEffort: an older client that rejects the call must not
// produce an error that could interfere with playback.
func TestProtectSeedingIsBestEffort(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/setShareLimits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: srv.URL, Category: "seedstream",
	}})
	// Must simply return, not panic or block.
	mgr.ProtectSeeding(context.Background(), "box", "dddddddddddddddddddddddddddddddddddddddd")
}
