package torrent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"seedstream/pkg/core/config"
)

// anchorDaemon is a Transmission daemon holding one complete single-file
// torrent. It records every sequential_download_from_piece it is asked for,
// which is the call the opening anchor makes.
type anchorDaemon struct {
	mu      sync.Mutex
	anchors []int
	hash    string
}

func (d *anchorDaemon) anchorCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.anchors)
}

func (d *anchorDaemon) server(t *testing.T) *httptest.Server {
	t.Helper()
	const totalPieces = 16
	// Every piece on disk: MSB-first, two bytes of 0xFF.
	bitfield := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xFF})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "torrent-get":
			fmt.Fprintf(w, `{"result":"success","arguments":{"torrents":[{
				"id":1,"hashString":%q,"name":"Thing.S01E01.1080p","totalSize":%d,
				"percentDone":1.0,"status":6,"downloadDir":"/downloads",
				"addedDate":1000,"doneDate":2000,"activityDate":2000,
				"peersSendingToUs":30,"pieceCount":%d,"pieceSize":%d,
				"labels":["seedstream"],"pieces":%q,"sequential_download":true,
				"trackerStats":[{"announce":"http://tr/a","lastAnnounceSucceeded":true,"seederCount":80,"leecherCount":2}],
				"files":[{"name":"Thing.S01E01.1080p.mkv","length":%d,"bytesCompleted":%d,"begin_piece":0,"end_piece":%d}],
				"fileStats":[{"bytesCompleted":%d,"wanted":true,"priority":0}]
			}]}}`,
				d.hash, totalPieces*(1<<20), totalPieces, 1<<20, bitfield,
				totalPieces*(1<<20), totalPieces*(1<<20), totalPieces, totalPieces*(1<<20))
		case "torrent-set":
			if piece, ok := req.Arguments["sequential_download_from_piece"]; ok {
				d.mu.Lock()
				d.anchors = append(d.anchors, int(piece.(float64)))
				d.mu.Unlock()
			}
			fmt.Fprint(w, `{"result":"success","arguments":{}}`)
		default:
			fmt.Fprint(w, `{"result":"success","arguments":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOpeningAnchorIsPlacedOncePerTorrent is the regression test for the
// re-firing anchor. anchoredHead is a per-prepare local, so every player retry
// — a reconnect, a new range request, a failover that comes back to the same
// release — used to re-anchor the download at the file's first piece, throwing
// away whatever anchor the previous attempt had reached. The anchor lives in
// the download client, not in one prepare call, so the latch has to as well.
func TestOpeningAnchorIsPlacedOncePerTorrent(t *testing.T) {
	rel := prepareTestRelease()
	d := &anchorDaemon{hash: rel.InfoHash}
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "transmission", URL: d.server(t).URL, Category: "seedstream",
	}})
	// The latch is process-wide, so a torrent another test anchored must not
	// leak into this one and vice versa.
	defer openingAnchors.Delete(playbackSelectionKey(
		config.NormalizeTorrentClientType("transmission")+":"+mgr.clients[0].cfg.URL, rel.InfoHash))

	for attempt := 1; attempt <= 3; attempt++ {
		res, err := mgr.PrepareForPlayback(context.Background(), rel, 1, 1,
			1<<20, PlaybackProfile{}, 10*time.Second, nil)
		if err != nil {
			t.Fatalf("attempt %d: prepare failed: %v", attempt, err)
		}
		mgr.ReleasePlayback(res)
	}

	if got := d.anchorCount(); got != 1 {
		t.Fatalf("the download was re-anchored %d times across three prepares; the opening anchor must be placed once per torrent", got)
	}
	if d.anchors[0] != 0 {
		t.Errorf("anchored at piece %d, want the video's first piece (0)", d.anchors[0])
	}
}

// TestFloorPercentNeverClaimsCompletion: "downloaded 100.0% of the file but
// not the first 16777216 bytes" reads as "complete but broken", and sends
// whoever reads it hunting an availability bug that is not there. A file at
// 99.963% must report as incomplete.
func TestFloorPercentNeverClaimsCompletion(t *testing.T) {
	cases := []struct {
		progress float64
		want     string
	}{
		{0.99963, "99.96"},
		{0.999999, "99.99"},
		{1.0, "100.00"},
		{0.5, "50.00"},
		{0, "0.00"},
		{-1, "0.00"}, // "unknown" must not print as a negative percentage
	}
	for _, tc := range cases {
		if got := fmt.Sprintf("%.2f", floorPercent(tc.progress)); got != tc.want {
			t.Errorf("floorPercent(%v) printed %s%%, want %s%%", tc.progress, got, tc.want)
		}
	}
}
