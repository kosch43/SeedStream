package transmission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"seedstream/pkg/torrent/tclient"
)

// mockDaemon is a Transmission RPC daemon. It records every request so the wire
// format can be asserted, and enforces the session-token handshake, since a
// client that does not implement it never gets past its first call.
type mockDaemon struct {
	requests  []rpcRequest
	rawBodies []map[string]any
	handshake int64 // times a 409 was issued
	token     string
	torrent   map[string]any
	// failFields, when set, makes torrent-get reject any request naming one of
	// these fields, imitating a daemon older than the field.
	failFields map[string]bool
}

func (d *mockDaemon) server(t *testing.T) *httptest.Server {
	t.Helper()
	d.token = "test-session-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != d.token {
			atomic.AddInt64(&d.handshake, 1)
			w.Header().Set("X-Transmission-Session-Id", d.token)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var raw map[string]any
		var req rpcRequest
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		_ = json.Unmarshal(body, &raw)
		_ = json.Unmarshal(body, &req)
		d.requests = append(d.requests, req)
		if args, ok := raw["arguments"].(map[string]any); ok {
			d.rawBodies = append(d.rawBodies, args)
		} else {
			d.rawBodies = append(d.rawBodies, map[string]any{})
		}

		switch req.Method {
		case "torrent-get":
			args, _ := raw["arguments"].(map[string]any)
			if fields, ok := args["fields"].([]any); ok {
				for _, f := range fields {
					if name, _ := f.(string); d.failFields[name] {
						fmt.Fprintf(w, `{"result":"unknown field: %s"}`, name)
						return
					}
				}
			}
			payload, _ := json.Marshal(map[string]any{
				"result":    "success",
				"arguments": map[string]any{"torrents": []any{d.torrent}},
			})
			w.Write(payload)
		case "session-stats":
			fmt.Fprint(w, `{"result":"success","arguments":{"downloadSpeed":1000,"uploadSpeed":2000,"current-stats":{"uploadedBytes":50,"downloadedBytes":70}}}`)
		default:
			fmt.Fprint(w, `{"result":"success","arguments":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (d *mockDaemon) client(t *testing.T) *Client {
	return New(Options{BaseURL: d.server(t).URL, Category: "seedstream"})
}

// argsFor returns the arguments of the last request with the given method.
func (d *mockDaemon) argsFor(method string) map[string]any {
	for i := len(d.requests) - 1; i >= 0; i-- {
		if d.requests[i].Method == method {
			return d.rawBodies[i]
		}
	}
	return nil
}

const testHash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

func baseTorrent() map[string]any {
	return map[string]any{
		"id": 1, "hashString": testHash, "name": "Thing.S01E01.1080p",
		"totalSize": 16 << 20, "percentDone": 0.5, "status": 4,
		"downloadDir": "/downloads", "addedDate": 1000, "doneDate": 0,
		"activityDate": 2000, "secondsSeeding": 3600, "uploadRatio": 1.5,
		"uploadedEver": 100, "downloadedEver": 200,
		"rateDownload": 5000, "rateUpload": 600,
		"peersSendingToUs": 4, "pieceCount": 16, "pieceSize": 1 << 20,
		"labels": []any{"seedstream"},
		"trackerStats": []any{
			map[string]any{"announce": "http://tr/a", "lastAnnounceSucceeded": true,
				"lastAnnounceTime": 1234, "seederCount": 60, "leecherCount": 3},
		},
	}
}

// TestSessionHandshakeIsTransparent: Transmission answers the first request of
// a session with 409 and the token to use. That is the handshake, not a
// failure, so it must be retried automatically — a client that treats it as an
// error never completes a single call.
func TestSessionHandshakeIsTransparent(t *testing.T) {
	d := &mockDaemon{torrent: baseTorrent()}
	c := d.client(t)

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping should survive the session handshake: %v", err)
	}
	if got := atomic.LoadInt64(&d.handshake); got != 1 {
		t.Fatalf("expected exactly one handshake, got %d", got)
	}
	// The token is remembered, so later calls do not repeat it.
	if _, err := c.Get(context.Background(), testHash); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := atomic.LoadInt64(&d.handshake); got != 1 {
		t.Fatalf("the token should be reused, got %d handshakes", got)
	}
}

// TestSequentialFieldsAreSnakeCase is the trap in this API. Every other field
// exists in both camelCase and snake_case, but the sequential ones were added
// after the naming changed and exist ONLY as snake_case. Transmission drops
// unknown arguments silently, so "sequentialDownload" would be accepted,
// ignored, and leave the torrent downloading rarest-first with nothing to show.
func TestSequentialFieldsAreSnakeCase(t *testing.T) {
	d := &mockDaemon{torrent: baseTorrent()}
	c := d.client(t)

	if err := c.Add(context.Background(), tclient.AddOptions{URL: "magnet:?xt=urn:btih:" + testHash, Sequential: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	args := d.argsFor("torrent-add")
	if _, ok := args["sequential_download"]; !ok {
		t.Errorf("torrent-add must send sequential_download, got keys %v", keys(args))
	}
	if _, ok := args["sequentialDownload"]; ok {
		t.Error("camelCase sequentialDownload is not a field Transmission knows; it would be silently ignored")
	}

	if err := c.SequentialFromPiece(context.Background(), testHash, 42); err != nil {
		t.Fatalf("SequentialFromPiece: %v", err)
	}
	args = d.argsFor("torrent-set")
	if got, ok := args["sequential_download_from_piece"]; !ok || got.(float64) != 42 {
		t.Errorf("expected sequential_download_from_piece=42, got %v from keys %v", got, keys(args))
	}
}

// TestPieceBitfieldDecodes: Transmission reports which pieces are on disk as a
// base64 bitfield, most significant bit first. Getting the bit order wrong
// would report the wrong pieces as present, which is exactly the failure that
// serves a player zeros from a hole.
func TestPieceBitfieldDecodes(t *testing.T) {
	// 16 pieces: the first 5 on disk, then a hole, then piece 8.
	var raw [2]byte
	for _, p := range []int{0, 1, 2, 3, 4, 8} {
		raw[p/8] |= 1 << (7 - uint(p%8))
	}
	tor := baseTorrent()
	tor["pieces"] = base64.StdEncoding.EncodeToString(raw[:])
	d := &mockDaemon{torrent: tor}
	c := d.client(t)

	states, err := c.PieceStates(context.Background(), testHash)
	if err != nil {
		t.Fatalf("PieceStates: %v", err)
	}
	if len(states) != 16 {
		t.Fatalf("expected 16 piece states, got %d", len(states))
	}
	for i, want := range map[int]int{0: 2, 4: 2, 5: 0, 7: 0, 8: 2, 15: 0} {
		if states[i] != want {
			t.Errorf("piece %d: got state %d, want %d", i, states[i], want)
		}
		_ = want
	}
}

// TestFilesDeriveAPieceRange: Transmission does not report which pieces a file
// spans, but files are laid out end to end so it can be computed. Without it
// the availability checker falls back to refusing any incomplete file, and
// streaming a partly-downloaded torrent stops working entirely.
func TestFilesDeriveAPieceRange(t *testing.T) {
	tor := baseTorrent()
	tor["files"] = []any{
		map[string]any{"name": "a.nfo", "length": 1 << 20, "bytesCompleted": 1 << 20},
		map[string]any{"name": "video.mkv", "length": 15 << 20, "bytesCompleted": 5 << 20},
	}
	tor["fileStats"] = []any{
		map[string]any{"bytesCompleted": 1 << 20, "wanted": true, "priority": 0},
		map[string]any{"bytesCompleted": 5 << 20, "wanted": true, "priority": 0},
	}
	d := &mockDaemon{torrent: tor}
	c := d.client(t)

	files, err := c.Files(context.Background(), testHash)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	// 1 MiB pieces: a.nfo is piece 0, video.mkv spans pieces 1..15.
	if got := files[0].PieceRange; len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Errorf("first file piece range %v, want [0 0]", got)
	}
	if got := files[1].PieceRange; len(got) != 2 || got[0] != 1 || got[1] != 15 {
		t.Errorf("video piece range %v, want [1 15]", got)
	}
	if files[1].Progress < 0.32 || files[1].Progress > 0.34 {
		t.Errorf("video progress %.3f, want ~0.333", files[1].Progress)
	}
}

// TestSwarmSeedersComeFromTheTracker: the seeder floor is judged on the swarm
// size, not the connection count. Transmission reports it per tracker, and this
// must reach TorrentInfo where SeedStream's own checks read it.
func TestSwarmSeedersComeFromTheTracker(t *testing.T) {
	d := &mockDaemon{torrent: baseTorrent()}
	c := d.client(t)

	info, err := c.Get(context.Background(), testHash)
	if err != nil || info == nil {
		t.Fatalf("Get: %v", err)
	}
	swarm, known := info.SwarmSeeders()
	if !known || swarm != 60 {
		t.Fatalf("swarm seeders = %d, known=%v; want 60, true", swarm, known)
	}
	if info.NumSeeds != 4 {
		t.Errorf("connected seeds %d, want 4 — this is a different number and must stay distinct", info.NumSeeds)
	}
}

// TestUnscrapedSwarmIsUnknownNotEmpty: a tracker reporting -1 has not been
// scraped. Treated as zero it would fail the seeder floor and reject a healthy
// torrent, which is the same trap qBittorrent's num_complete carries.
func TestUnscrapedSwarmIsUnknownNotEmpty(t *testing.T) {
	tor := baseTorrent()
	tor["trackerStats"] = []any{
		map[string]any{"announce": "http://tr/a", "lastAnnounceTime": 0, "seederCount": -1},
	}
	d := &mockDaemon{torrent: tor}
	c := d.client(t)

	info, err := c.Get(context.Background(), testHash)
	if err != nil || info == nil {
		t.Fatalf("Get: %v", err)
	}
	if _, known := info.SwarmSeeders(); known {
		t.Fatal("a tracker that has not been scraped reports -1, which is unknown, not an empty swarm")
	}
}

// TestShareLimitsPinToUnlimited: SeedStream pins limits so the client never
// stops seeding on its own and cuts a hit-and-run obligation short.
// Transmission expresses that as a mode rather than a sentinel, so the
// translation has to be right or the pinning silently does nothing.
func TestShareLimitsPinToUnlimited(t *testing.T) {
	d := &mockDaemon{torrent: baseTorrent()}
	c := d.client(t)

	if err := c.SetShareLimits(context.Background(), testHash,
		tclient.NoShareLimit, tclient.NoShareLimit, tclient.NoShareLimit); err != nil {
		t.Fatalf("SetShareLimits: %v", err)
	}
	args := d.argsFor("torrent-set")
	if got := args["seedRatioMode"]; got != float64(seedModeUnlimited) {
		t.Errorf("seedRatioMode = %v, want %d (unlimited)", got, seedModeUnlimited)
	}
	if got := args["seedIdleMode"]; got != float64(seedModeUnlimited) {
		t.Errorf("seedIdleMode = %v, want %d (unlimited)", got, seedModeUnlimited)
	}
}

// TestOlderDaemonStillWorks: a daemon before 4.1 rejects the whole torrent-get
// when it names sequential_download. Losing the torrent entirely over an
// optional field would break streaming on every Transmission 4.0 seedbox, so
// the call is retried without it.
func TestOlderDaemonStillWorks(t *testing.T) {
	d := &mockDaemon{torrent: baseTorrent(), failFields: map[string]bool{"sequential_download": true}}
	c := d.client(t)

	info, err := c.Get(context.Background(), testHash)
	if err != nil {
		t.Fatalf("a pre-4.1 daemon must still be usable: %v", err)
	}
	if info == nil {
		t.Fatal("expected the torrent, got nil")
	}
	if info.SequentialDL {
		t.Error("a daemon that does not support sequential download must not report it as on")
	}
}

// TestListCategoryFiltersByLabel: Transmission has no categories, so
// SeedStream's own torrents are marked with a label. Everything the watchdog
// and Cerberus manage is found through this, so a torrent without the label
// must not be picked up — it belongs to someone else.
func TestListCategoryFiltersByLabel(t *testing.T) {
	tor := baseTorrent()
	tor["labels"] = []any{"someone-elses"}
	d := &mockDaemon{torrent: tor}
	c := d.client(t)

	list, err := c.ListCategory(context.Background())
	if err != nil {
		t.Fatalf("ListCategory: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a torrent without SeedStream's label is not SeedStream's, got %d", len(list))
	}
}

// TestUsesTheLegacyEnvelope: Transmission 4.1 added a JSON-RPC 2.0 protocol and
// deprecated this one, but 4.0.x — what most seedboxes still run — only speaks
// the old one, and 4.1 still accepts it. Speaking the new protocol would work
// on exactly one release.
func TestUsesTheLegacyEnvelope(t *testing.T) {
	d := &mockDaemon{torrent: baseTorrent()}
	c := d.client(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if len(d.requests) == 0 || d.requests[0].Method != "session-get" {
		t.Fatalf("expected a legacy {method,arguments} envelope, got %+v", d.requests)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
