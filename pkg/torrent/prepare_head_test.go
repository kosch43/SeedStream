package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/torrent/qbittorrent"
)

// headQBit is a mock qBittorrent that reports an explicit piece bitmap, so the
// prepare loop can be tested against a head that is fragmented rather than
// merely incomplete. Real seedboxes produce exactly this: sequential download
// is a preference, not a guarantee, so pieces land out of order and the file's
// byte count runs far ahead of its contiguous prefix.
type headQBit struct {
	pieceSize   int64
	totalPieces int
	downloaded  map[int]bool // piece index -> on disk
}

func (q *headQBit) fileSize() int64 { return q.pieceSize * int64(q.totalPieces) }

func (q *headQBit) progress() float64 {
	n := 0
	for i := 0; i < q.totalPieces; i++ {
		if q.downloaded[i] {
			n++
		}
	}
	return float64(n) / float64(q.totalPieces)
}

func (q *headQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%s","name":"Thing.S01E01.1080p","size":%d,"progress":%v,"state":"downloading","category":"seedstream","save_path":"/downloads","num_seeds":50}]`,
			hash, q.fileSize(), q.progress())
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"index":0,"name":"Thing.S01E01.1080p.mkv","size":%d,"progress":%v,"priority":1,"piece_range":[0,%d]}]`,
			q.fileSize(), q.progress(), q.totalPieces-1)
	})
	mux.HandleFunc("/api/v2/torrents/properties", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"piece_size":%d,"pieces_num":%d,"total_size":%d}`, q.pieceSize, q.totalPieces, q.fileSize())
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		states := make([]string, q.totalPieces)
		for i := range states {
			if q.downloaded[i] {
				states[i] = strconv.Itoa(qbittorrent.PieceDownloaded)
			} else {
				states[i] = "0"
			}
		}
		fmt.Fprint(w, "["+strings.Join(states, ",")+"]")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func headManager(t *testing.T, q *headQBit) *Manager {
	t.Helper()
	return NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})
}

// TestPrepareRejectsFragmentedHead is the regression for the field report: a
// 51 GB remux sat at 84% with only four contiguous pieces at the front, yet
// prepare declared it ready because progress*size cleared the buffer target.
// Playback began, drained those pieces in seconds and stalled.
//
// Byte counts say how much is downloaded, never where. Only the piece bitmap
// answers the question that matters — is the head continuous?
func TestPrepareRejectsFragmentedHead(t *testing.T) {
	const pieceSize = 1 << 20
	q := &headQBit{pieceSize: pieceSize, totalPieces: 16, downloaded: map[int]bool{}}
	// 14 of 16 pieces on disk (87.5%), but pieces 2 and 3 are missing — so the
	// continuous run from the start of the file is only 2 MiB.
	for i := 0; i < 16; i++ {
		q.downloaded[i] = i != 2 && i != 3
	}

	mgr := headManager(t, q)
	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 8<<20, 4*time.Second, nil)
	if err == nil {
		t.Fatal("a file with holes inside the head must not be reported ready for playback")
	}
	if !contains(err.Error(), "out of order") {
		t.Fatalf("the error should explain that pieces arrived out of order, got: %v", err)
	}
}

// TestPrepareAcceptsContiguousHead is the other half: the same amount of data,
// arranged continuously from byte zero, must be served without waiting for the
// rest of the torrent.
func TestPrepareAcceptsContiguousHead(t *testing.T) {
	const pieceSize = 1 << 20
	q := &headQBit{pieceSize: pieceSize, totalPieces: 16, downloaded: map[int]bool{}}
	for i := 0; i < 8; i++ { // exactly the first 8 MiB, nothing else
		q.downloaded[i] = true
	}

	mgr := headManager(t, q)
	start := time.Now()
	res, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 8<<20, 20*time.Second, nil)
	if err != nil {
		t.Fatalf("a continuous 8 MiB head is playable and should prepare: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("prepare waited %v for a head that was already on disk", elapsed)
	}
	if res.Name != "Thing.S01E01.1080p.mkv" {
		t.Fatalf("unexpected file %q", res.Name)
	}
}
