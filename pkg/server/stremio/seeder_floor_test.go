package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
	"seedstream/pkg/torrent"
)

// presenceQBit reports one torrent at a configurable progress, which is all the
// seeder floor needs in order to decide whether the data is already local.
func presenceQBit(t *testing.T, hash string, progress float64) *torrent.Manager {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%s","name":"Thing","size":1000,"progress":%v,"state":"uploading","category":"seedstream","save_path":"/downloads"}]`,
			hash, progress)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return torrent.NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: srv.URL, Category: "seedstream",
	}})
}

func floorServer(t *testing.T, min int, mgr *torrent.Manager) *Server {
	t.Helper()
	return &Server{config: &config.Config{MinSeeders: &min}, torrentManager: mgr}
}

const floorHash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

func thinRelease() *release.Release {
	return &release.Release{
		Title: "Thing.S01E01.1080p", Protocol: "torrent",
		InfoHash: floorHash, Seeders: 2, SeedersKnown: true,
	}
}

// TestSeederFloorRejectsThinSwarm is the ordinary case: a release the seedbox
// does not hold, with a swarm below the minimum, must not be played.
func TestSeederFloorRejectsThinSwarm(t *testing.T) {
	s := floorServer(t, 10, presenceQBit(t, floorHash, 0.4))
	if err := s.releasePassesSeederFloor(context.Background(), thinRelease(), floorHash); err == nil {
		t.Fatal("a 2-seeder release must be refused when the minimum is 10")
	}
}

// TestSeederFloorAllowsACompletedTorrent is the exemption. A finished torrent is
// read from local disk — its swarm could be empty and playback would be
// unaffected, because there is nothing left to download. Refusing it on a seeder
// count would reject the fastest copy available, which is backwards.
func TestSeederFloorAllowsACompletedTorrent(t *testing.T) {
	s := floorServer(t, 10, presenceQBit(t, floorHash, 1))
	if err := s.releasePassesSeederFloor(context.Background(), thinRelease(), floorHash); err != nil {
		t.Fatalf("an already-downloaded title must play regardless of its swarm: %v", err)
	}
}

// TestSeederFloorStillJudgesAPartialTorrent: present on the seedbox is not the
// same as complete. A half-downloaded torrent still needs its swarm to finish.
func TestSeederFloorStillJudgesAPartialTorrent(t *testing.T) {
	s := floorServer(t, 10, presenceQBit(t, floorHash, 0.5))
	if err := s.releasePassesSeederFloor(context.Background(), thinRelease(), floorHash); err == nil {
		t.Fatal("a partially downloaded torrent still depends on its swarm")
	}
}

// TestSeederFloorIsOffWhenUnset keeps existing setups unchanged.
func TestSeederFloorIsOffWhenUnset(t *testing.T) {
	s := floorServer(t, 0, presenceQBit(t, floorHash, 0.4))
	if err := s.releasePassesSeederFloor(context.Background(), thinRelease(), floorHash); err != nil {
		t.Fatalf("with no minimum configured nothing should be refused: %v", err)
	}
}
