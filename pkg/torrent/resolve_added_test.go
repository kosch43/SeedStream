package torrent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/torrent/qbittorrent"
)

// categoryQBit serves a fixed torrent list for /torrents/info.
type categoryQBit struct{ list []qbittorrent.TorrentInfo }

func (q *categoryQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		if h := r.URL.Query().Get("hashes"); h != "" {
			for _, ti := range q.list {
				if ti.Hash == h {
					_ = json.NewEncoder(w).Encode([]qbittorrent.TorrentInfo{ti})
					return
				}
			}
			fmt.Fprint(w, "[]")
			return
		}
		_ = json.NewEncoder(w).Encode(q.list)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func categoryManager(t *testing.T, q *categoryQBit) (*Manager, *qbittorrent.Client) {
	t.Helper()
	srv := q.server(t)
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: srv.URL, Category: "seedstream",
	}})
	return mgr, qbittorrent.New(qbittorrent.Options{BaseURL: srv.URL, Category: "seedstream"})
}

// TestResolveAddedIgnoresConcurrentlyAddedTorrent is the regression test for the
// wrong-content bug. A release with no info hash used to resolve to "the newest
// torrent in the category", so a torrent another request added moments earlier
// was served instead — the session and stream list stayed correct while the
// bytes came from an unrelated title.
func TestResolveAddedIgnoresConcurrentlyAddedTorrent(t *testing.T) {
	ours := qbittorrent.TorrentInfo{
		Hash: "1111111111111111111111111111111111111111",
		Name: "Rick.and.Morty.S05E01.1080p.WEBRip.x265-RARBG", AddedOn: 100,
	}
	// Another request's torrent, added later, so it is the "newest".
	other := qbittorrent.TorrentInfo{
		Hash: "2222222222222222222222222222222222222222",
		Name: "The.Shawshank.Redemption.1994.1080p.BluRay.x265", AddedOn: 999,
	}
	q := &categoryQBit{list: []qbittorrent.TorrentInfo{other, ours}}
	mgr, c := categoryManager(t, q)

	// The other torrent already existed when we issued our add.
	before := map[string]bool{other.Hash: true}

	got, err := mgr.resolveAdded(context.Background(), c, "", before, ours.Name)
	if err != nil {
		t.Fatalf("resolveAdded: %v", err)
	}
	if got.Hash != ours.Hash {
		t.Fatalf("resolved the wrong torrent: got %q (%s), want %q",
			got.Name, got.Hash, ours.Name)
	}
}

// TestResolveAddedDisambiguatesByTitle: when several torrents appear at once
// (concurrent playback requests), the one matching the release title wins.
func TestResolveAddedDisambiguatesByTitle(t *testing.T) {
	ours := qbittorrent.TorrentInfo{
		Hash: "3333333333333333333333333333333333333333",
		Name: "Rick.and.Morty.S05E01.1080p.WEBRip.x265-RARBG", AddedOn: 10,
	}
	theirs := qbittorrent.TorrentInfo{
		Hash: "4444444444444444444444444444444444444444",
		Name: "Breaking.Bad.S03E07.1080p.BluRay.x265", AddedOn: 11,
	}
	q := &categoryQBit{list: []qbittorrent.TorrentInfo{theirs, ours}}
	mgr, c := categoryManager(t, q)

	got, err := mgr.resolveAdded(context.Background(), c, "", map[string]bool{}, ours.Name)
	if err != nil {
		t.Fatalf("resolveAdded: %v", err)
	}
	if got.Hash != ours.Hash {
		t.Fatalf("title disambiguation failed: got %q want %q", got.Name, ours.Name)
	}
}

// TestResolveAddedFailsRatherThanGuess: if nothing that appeared resembles the
// release, resolveAdded must error instead of serving an unrelated torrent.
func TestResolveAddedFailsRatherThanGuess(t *testing.T) {
	a := qbittorrent.TorrentInfo{Hash: "5555555555555555555555555555555555555555", Name: "Some.Other.Movie.2019.1080p", AddedOn: 5}
	b := qbittorrent.TorrentInfo{Hash: "6666666666666666666666666666666666666666", Name: "Unrelated.Documentary.2020.720p", AddedOn: 6}
	q := &categoryQBit{list: []qbittorrent.TorrentInfo{a, b}}
	mgr, c := categoryManager(t, q)

	got, err := mgr.resolveAdded(context.Background(), c, "", map[string]bool{}, "Rick.and.Morty.S05E01.1080p.WEBRip")
	if err == nil {
		t.Fatalf("expected an error, but it guessed %q", got.Name)
	}
}

// TestResolveAddedPrefersKnownHash: when the release carries an info hash it is
// authoritative and no guessing happens at all.
func TestResolveAddedPrefersKnownHash(t *testing.T) {
	ours := qbittorrent.TorrentInfo{
		Hash: "7777777777777777777777777777777777777777",
		Name: "Rick.and.Morty.S05E01.1080p", AddedOn: 1,
	}
	newer := qbittorrent.TorrentInfo{Hash: "8888888888888888888888888888888888888888", Name: "Newer.Thing", AddedOn: 500}
	q := &categoryQBit{list: []qbittorrent.TorrentInfo{newer, ours}}
	mgr, c := categoryManager(t, q)

	got, err := mgr.resolveAdded(context.Background(), c, ours.Hash, map[string]bool{}, "anything")
	if err != nil {
		t.Fatalf("resolveAdded: %v", err)
	}
	if got.Hash != ours.Hash {
		t.Fatalf("known hash must win: got %s want %s", got.Hash, ours.Hash)
	}
}

// TestIsStalledForcedDL: force-started torrents bypass the queue but stall like
// any other, and were previously invisible to the watchdog.
func TestIsStalledForcedDL(t *testing.T) {
	threshold := 10 * time.Minute
	stuck := TorrentHealthEntry{
		State: "forcedDL", Progress: 0.2,
		AddedAt:      time.Now().Add(-2 * time.Hour),
		LastActivity: time.Now().Add(-90 * time.Minute),
	}
	if !isStalled(stuck, threshold) {
		t.Fatal("a forcedDL torrent with no activity for 90 minutes must be seen as stalled")
	}
	active := TorrentHealthEntry{
		State: "forcedDL", Progress: 0.2,
		AddedAt:      time.Now().Add(-2 * time.Hour),
		LastActivity: time.Now(),
	}
	if isStalled(active, threshold) {
		t.Fatal("an actively downloading forcedDL torrent must not be flagged")
	}
}
