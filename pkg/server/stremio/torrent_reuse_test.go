package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/release"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/torrent"
)

// reuseQBit is a mock qBittorrent that only knows about the hashes it is told
// about, so "torrent still on the seedbox" can be simulated per test.
type reuseQBit struct{ present map[string]float64 }

func (q *reuseQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		h := r.URL.Query().Get("hashes")
		progress, ok := q.present[h]
		if !ok {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"hash":"%s","name":"n","size":1000,"progress":%v,"state":"uploading","category":"seedstream","save_path":"/downloads"}]`, h, progress)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newReuseServer(t *testing.T, q *reuseQBit) (*Server, *cerberus.Client) {
	t.Helper()
	store, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	cer := cerberus.New(store)
	mgr := torrent.NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})
	return &Server{cerberusClient: cer, torrentManager: mgr, config: &config.Config{}}, cer
}

const (
	hashKnown    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashSelected = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func episodeIDs(imdb string) cerberus.ContentIDs {
	return cerberus.ContentIDs{ImdbID: imdb, Season: 5, Episode: 1}
}

func selectedRelease() *release.Release {
	return &release.Release{
		Title:    "Rick.and.Morty.S05E01.2160p.WEB.x265-OTHER",
		Protocol: "torrent",
		InfoHash: hashSelected,
	}
}

// TestReusesAlreadyDownloadedTorrent is the core behaviour: a title played
// before must replay from the torrent the seedbox already holds rather than
// starting a fresh download of a different release.
func TestReusesAlreadyDownloadedTorrent(t *testing.T) {
	q := &reuseQBit{present: map[string]float64{hashKnown: 1.0}} // known torrent complete
	s, cer := newReuseServer(t, q)

	ids := episodeIDs("tt2861424")
	if err := cer.RegisterTorrent(hashKnown, ids, "magnet:?xt=urn:btih:"+hashKnown,
		"Rick.and.Morty.S05E01.1080p.WEBRip.x265-RARBG", "Tracker"); err != nil {
		t.Fatalf("register: %v", err)
	}

	sel := selectedRelease()
	got := s.resolvePlayRelease(context.Background(), sel, hashSelected, ids, 5, 1)

	if got == sel {
		t.Fatal("expected the already-downloaded torrent to be reused")
	}
	if got.InfoHash != hashKnown {
		t.Fatalf("reused wrong torrent: got %s want %s", got.InfoHash, hashKnown)
	}
}

// TestNeverReusesAcrossDifferentContent is the correctness guard: a torrent
// registered for one title must never be served for another.
func TestNeverReusesAcrossDifferentContent(t *testing.T) {
	q := &reuseQBit{present: map[string]float64{hashKnown: 1.0}}
	s, cer := newReuseServer(t, q)

	// Registered for one show...
	if err := cer.RegisterTorrent(hashKnown, episodeIDs("tt2861424"), "", "Rick.and.Morty.S05E01.1080p", "T"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// ...requested for a different show, same season/episode numbering.
	sel := selectedRelease()
	got := s.resolvePlayRelease(context.Background(), sel, hashSelected, episodeIDs("tt0903747"), 5, 1)

	if got != sel {
		t.Fatalf("must not reuse a torrent registered for different content (got hash %s)", got.InfoHash)
	}
}

// TestNoReuseWhenRememberedTorrentIsGone: the registry outlives the data, so a
// torrent deleted from the seedbox must fall back to the normal path.
func TestNoReuseWhenRememberedTorrentIsGone(t *testing.T) {
	q := &reuseQBit{present: map[string]float64{}} // nothing on the seedbox
	s, cer := newReuseServer(t, q)

	ids := episodeIDs("tt1111111")
	if err := cer.RegisterTorrent(hashKnown, ids, "", "Rick.and.Morty.S05E01.1080p", "T"); err != nil {
		t.Fatalf("register: %v", err)
	}

	sel := selectedRelease()
	if got := s.resolvePlayRelease(context.Background(), sel, hashSelected, ids, 5, 1); got != sel {
		t.Fatal("a remembered torrent that is no longer present must not be reused")
	}
}

// TestKeepsSelectedReleaseWhenAlreadyPresent: the viewer's own pick wins when it
// is already on the seedbox — switching would override their quality choice for
// no gain.
func TestKeepsSelectedReleaseWhenAlreadyPresent(t *testing.T) {
	q := &reuseQBit{present: map[string]float64{hashKnown: 1.0, hashSelected: 1.0}}
	s, cer := newReuseServer(t, q)

	ids := episodeIDs("tt2222222")
	if err := cer.RegisterTorrent(hashKnown, ids, "", "Rick.and.Morty.S05E01.1080p", "T"); err != nil {
		t.Fatalf("register: %v", err)
	}

	sel := selectedRelease()
	if got := s.resolvePlayRelease(context.Background(), sel, hashSelected, ids, 5, 1); got != sel {
		t.Fatal("the selected release is already present and must be kept")
	}
}

// TestNoReuseOfMismatchedRememberedTitle: defence in depth — even a registry
// entry filed under the right content is re-checked against the episode guard.
func TestNoReuseOfMismatchedRememberedTitle(t *testing.T) {
	q := &reuseQBit{present: map[string]float64{hashKnown: 1.0}}
	s, cer := newReuseServer(t, q)

	ids := episodeIDs("tt3333333")
	if err := cer.RegisterTorrent(hashKnown, ids, "",
		"The.Shawshank.Redemption.1994.1080p.BluRay.x265", "T"); err != nil {
		t.Fatalf("register: %v", err)
	}

	sel := selectedRelease()
	if got := s.resolvePlayRelease(context.Background(), sel, hashSelected, ids, 5, 1); got != sel {
		t.Fatalf("a remembered entry whose title cannot contain S05E01 must not be reused (got %q)", got.Title)
	}
}
