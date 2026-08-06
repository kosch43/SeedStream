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

// swarmQBit is a mock qBittorrent that reports a configurable swarm. numComplete
// is rendered as the tracker's scrape result; -1 means "not scraped yet", and
// omit=true drops the field entirely, as an older server would.
type swarmQBit struct {
	numComplete int
	numSeeds    int
	omit        bool
	progress    float64
}

func (q *swarmQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fields := map[string]any{
			"hash": hash, "name": "Thing.S01E01.1080p", "size": 16 << 20,
			"progress": q.progress, "state": "downloading", "category": "seedstream",
			"save_path": "/downloads", "num_seeds": q.numSeeds,
		}
		if !q.omit {
			fields["num_complete"] = q.numComplete
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{fields})
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"index":0,"name":"Thing.S01E01.1080p.mkv","size":%d,"progress":%v,"priority":1,"piece_range":[0,15]}]`,
			16<<20, q.progress)
	})
	mux.HandleFunc("/api/v2/torrents/properties", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"piece_size":%d,"pieces_num":16,"total_size":%d}`, 1<<20, 16<<20)
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		// Only the first piece: never enough head to finish preparing, so the
		// swarm check is what decides the outcome.
		fmt.Fprint(w, "[2,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func swarmManager(t *testing.T, q *swarmQBit, minSeeders int) *Manager {
	t.Helper()
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})
	mgr.MinSeeders = minSeeders
	return mgr
}

// TestSwarmSeedersDistinguishesUnknownFromEmpty is the trap in this data: a
// server that omits num_complete would decode it as 0, and a swarm of "0
// seeders" would fail every check — rejecting every torrent on every older
// qBittorrent.
func TestSwarmSeedersDistinguishesUnknownFromEmpty(t *testing.T) {
	zero := 0
	minusOne := -1
	forty := 40

	if n, known := (&qbittorrent.TorrentInfo{}).SwarmSeeders(); known {
		t.Fatalf("a missing field must read as unknown, got %d known", n)
	}
	if _, known := (&qbittorrent.TorrentInfo{NumComplete: &minusOne}).SwarmSeeders(); known {
		t.Fatal("-1 means the tracker has not been scraped yet, not a dead swarm")
	}
	if n, known := (&qbittorrent.TorrentInfo{NumComplete: &zero}).SwarmSeeders(); !known || n != 0 {
		t.Fatalf("a reported zero is a known, empty swarm; got %d known=%v", n, known)
	}
	if n, known := (&qbittorrent.TorrentInfo{NumComplete: &forty}).SwarmSeeders(); !known || n != 40 {
		t.Fatalf("SwarmSeeders() = %d, %v; want 40, true", n, known)
	}
}

// TestPrepareRejectsSwarmBelowTheMinimum: the tracker's live count is below the
// floor, so the torrent must not be served — regardless of the fact that it is
// downloading, which the old rule required before it would reject anything.
func TestPrepareRejectsSwarmBelowTheMinimum(t *testing.T) {
	q := &swarmQBit{numComplete: 3, numSeeds: 3, progress: 0.06}
	mgr := swarmManager(t, q, 10)

	start := time.Now()
	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 8<<20, 90*time.Second, nil)
	if err == nil {
		t.Fatal("a swarm of 3 must not be served when the minimum is 10")
	}
	if !contains(err.Error(), "below the 10 required") {
		t.Fatalf("the error should name the shortfall, got: %v", err)
	}
	// Rejected shortly after the grace period, not after the full timeout.
	if elapsed := time.Since(start); elapsed > seedCheckGrace+10*time.Second {
		t.Fatalf("took %v to reject — it should fail soon after the grace period", elapsed)
	}
}

// TestPrepareAcceptsHealthySwarmWithFewConnections is the over-blocking guard.
// BitTorrent connects to a subset of the swarm, so 4 connected out of 60 is
// entirely normal. Judging on the connected count would reject a healthy torrent.
func TestPrepareAcceptsHealthySwarmWithFewConnections(t *testing.T) {
	q := &swarmQBit{numComplete: 60, numSeeds: 4, progress: 0.06}
	mgr := swarmManager(t, q, 10)

	// Prepare cannot succeed (the head never fills), so the test is that it
	// times out on buffering rather than being rejected for its swarm.
	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		8<<20, seedCheckGrace+6*time.Second, nil)
	if err == nil {
		t.Fatal("expected a buffering timeout")
	}
	if contains(err.Error(), "seeder") {
		t.Fatalf("a 60-seed swarm reached through 4 connections is healthy, got: %v", err)
	}
}

// TestPrepareWithoutAScrapeFallsBackToProgress: with no tracker count to go on,
// the connected count alone is not enough to condemn a torrent — it under-reports
// by design — so it is only trusted when the download has also stopped advancing.
func TestPrepareWithoutAScrapeFallsBackToProgress(t *testing.T) {
	q := &swarmQBit{omit: true, numSeeds: 2, progress: 0.06}
	mgr := swarmManager(t, q, 10)

	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		8<<20, 90*time.Second, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !contains(err.Error(), "not advancing") {
		t.Fatalf("without a scrape the rejection must rest on stalled progress, got: %v", err)
	}
}
