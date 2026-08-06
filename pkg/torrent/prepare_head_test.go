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
	q := &headQBit{pieceSize: pieceSize, totalPieces: 160, downloaded: map[int]bool{}}
	// 158 of 160 pieces on disk (98.75%), but pieces 8 and 9 are missing — so
	// the continuous run from the start of the file is only 8 MiB.
	for i := 0; i < 160; i++ {
		q.downloaded[i] = i != 8 && i != 9
	}

	mgr := headManager(t, q)
	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 32<<20, 4*time.Second, nil)
	if err == nil {
		t.Fatal("a file with holes inside the head must not be reported ready for playback")
	}
	if !contains(err.Error(), "out of order") {
		t.Fatalf("the error should explain that pieces arrived out of order, got: %v", err)
	}
}

// TestPrepareStreamsAtTenPercent is the guarantee: a torrent whose first tenth
// is downloaded, in order, plays. A tenth of a film is minutes of video, which
// is more headroom than playback needs, so nothing further should be waited on
// — not the rest of the file, and not a larger buffer some bitrate estimate
// asked for.
func TestPrepareStreamsAtTenPercent(t *testing.T) {
	const pieceSize = 1 << 20
	q := &headQBit{pieceSize: pieceSize, totalPieces: 160, downloaded: map[int]bool{}}
	for i := 0; i < 16; i++ { // exactly the first 10%, nothing else
		q.downloaded[i] = true
	}

	mgr := headManager(t, q)
	start := time.Now()
	// Ask for far more head than a tenth: the ceiling must bring it back down.
	res, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 120<<20, 20*time.Second, nil)
	if err != nil {
		t.Fatalf("a torrent 10%% downloaded in order must stream: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("prepare waited %v for a head that was already on disk", elapsed)
	}
	if res.Name != "Thing.S01E01.1080p.mkv" {
		t.Fatalf("unexpected file %q", res.Name)
	}
}

// TestRequiredHeadBytesCeiling pins the arithmetic behind that guarantee.
func TestRequiredHeadBytesCeiling(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name           string
		want, size, in int64
	}{
		// A 51 GB remux: the 384 MB cap is well under a tenth, so it stands.
		{"large file keeps its bitrate buffer", 384 << 20, 51 * gib, 384 << 20},
		// A 700 MB episode asked for an implausible 300 MB head: capped at 70 MB.
		{"small file is capped at a tenth", 70000000, 700000000, 300 << 20},
		// Unknown size: nothing to compute a ceiling from, pass the ask through.
		{"unknown size passes through", 16 << 20, 0, 16 << 20},
	}
	for _, tc := range cases {
		if got := requiredHeadBytes(tc.in, tc.size); got != tc.want {
			t.Errorf("%s: requiredHeadBytes(%d, %d) = %d, want %d", tc.name, tc.in, tc.size, got, tc.want)
		}
	}
}
