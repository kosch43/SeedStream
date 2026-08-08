package torrent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/torrent/tclient"
)

type streamingHeadQBit struct {
	hash              string
	progress          float64
	filePriorityCalls atomic.Int64
	reannounceCalls   atomic.Int64
}

func (q *streamingHeadQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().Unix()
		fmt.Fprintf(w, `[{"hash":%q,"name":"Show Season 1","size":1000,"progress":%v,"state":"downloading","category":"seedstream","added_on":%d,"last_activity":%d,"seq_dl":true,"f_l_piece_prio":true}]`,
			q.hash, q.progress, now, now)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"index":0,"name":"Show.S01E01.mkv","size":500,"progress":1,"priority":1,"piece_range":[0,9]},
			{"index":1,"name":"Show.S01E10.mkv","size":500,"progress":0.4,"priority":7,"piece_range":[90,99]}
		]`)
	})
	mux.HandleFunc("/api/v2/torrents/filePrio", func(w http.ResponseWriter, r *http.Request) {
		q.filePriorityCalls.Add(1)
	})
	mux.HandleFunc("/api/v2/torrents/reannounce", func(w http.ResponseWriter, r *http.Request) {
		q.reannounceCalls.Add(1)
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		states := make([]int, 100)
		// Torrent piece 0 belongs to episode 1 and is already downloaded. The
		// requested episode starts at piece 90, which is missing while five of
		// that episode's later pieces have completed.
		states[0] = tclient.PieceDownloaded
		for i := 91; i <= 95; i++ {
			states[i] = tclient.PieceDownloaded
		}
		if err := json.NewEncoder(w).Encode(states); err != nil {
			t.Errorf("encode piece states: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCerberusChecksQBitSelectedFileHead(t *testing.T) {
	q := &streamingHeadQBit{
		hash:     "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		progress: 0.9995,
	}
	wd := newTestWatchdog(t, q.server(t).URL)
	// Persist an older episode for this pack. Cerberus must follow qBittorrent's
	// live priority (E10), not mutate priorities from this stale record (E01).
	if err := wd.cerberus.RegisterTorrent(q.hash, cerberus.ContentIDs{
		ImdbID: "tt123", Season: 1, Episode: 1,
	}, "magnet:?xt=urn:btih:"+q.hash, "Show Season 1", "tracker"); err != nil {
		t.Fatalf("register torrent: %v", err)
	}

	wd.check(context.Background(), 10*time.Minute)

	if got := q.filePriorityCalls.Load(); got != 0 {
		t.Fatalf("watchdog made %d file-priority mutations from stale metadata, want 0", got)
	}
	if got := q.reannounceCalls.Load(); got != 1 {
		t.Fatalf("missing selected head received %d reannounce calls, want 1", got)
	}
	if !wd.headWarned[streamingHeadKey("seedbox", q.hash, 1)] {
		t.Fatal("Cerberus should detect the requested episode's missing first piece")
	}
}

func TestHeadPieceMissingRequiresLaterCompletedPieces(t *testing.T) {
	if !headPieceMissing(tclient.PieceDownloading, 20) {
		t.Fatal("a requested but incomplete first piece is not streamable yet")
	}
	if headPieceMissing(tclient.PieceNotDownloaded, streamingOrderMinDownloadedPieces-1) {
		t.Fatal("a fresh file does not have enough completed work to diagnose ordering")
	}
	if !headPieceMissing(tclient.PieceNotDownloaded, streamingOrderMinDownloadedPieces) {
		t.Fatal("a missing first piece after several later completions should be detected")
	}
}
