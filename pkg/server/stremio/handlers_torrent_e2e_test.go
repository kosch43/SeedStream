package stremio

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

const e2eTestHash = "0123456789abcdef0123456789abcdef01234567"

// newMockQBittorrent serves the subset of the qBittorrent WebUI API that the
// torrent manager uses: login, version, add, info, and files. The torrent
// "exists" once added (or immediately when preSeeded), reporting a single
// fully-downloaded video file stored in saveDir.
func newMockQBittorrent(t *testing.T, saveDir, fileName string, fileSize int64, preSeeded bool) *httptest.Server {
	t.Helper()
	added := preSeeded
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v5.0.0")
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		added = true
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !added {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"hash":%q,"name":"Test Movie","size":%d,"progress":1.0,"state":"uploading","save_path":%q,"category":"seedstream","added_on":%d,"seeding_time":3600,"last_activity":%d,"ratio":1.5,"uploaded":%d}]`,
			e2eTestHash, fileSize, saveDir, time.Now().Unix()-60, time.Now().Unix(), fileSize)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !added {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprintf(w, `[{"index":0,"name":%q,"size":%d,"progress":1.0,"priority":1}]`, fileName, fileSize)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTorrentE2EServer(t *testing.T, qbitURL, saveDir string) (*Server, *session.Manager) {
	t.Helper()
	cfg := &config.Config{
		AddonBaseURL: "http://127.0.0.1:7000",
		TorrentClients: []config.TorrentClientConfig{{
			Name:     "test-qbit",
			Type:     "qbittorrent",
			URL:      qbitURL,
			SavePath: saveDir,
		}},
	}
	sessMgr := session.NewManager(time.Minute)
	t.Cleanup(sessMgr.Shutdown)
	srv := &Server{
		manifest:          NewManifest("test"),
		version:           "test",
		baseURL:           cfg.AddonBaseURL,
		config:            cfg,
		sessionManager:    sessMgr,
		uniqueIndexerHits: map[string]int64{},
	}
	srv.torrentManager = torrent.NewManager(cfg.TorrentClients)
	return srv, sessMgr
}

func TestTorrentPlayEndToEnd(t *testing.T) {
	saveDir := t.TempDir()
	fileName := "Test.Movie.2024.1080p.mkv"
	content := make([]byte, 2*1024*1024)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(saveDir, fileName), content, 0o644); err != nil {
		t.Fatalf("write test video: %v", err)
	}

	qbit := newMockQBittorrent(t, saveDir, fileName, int64(len(content)), false)
	srv, sessMgr := newTorrentE2EServer(t, qbit.URL, saveDir)

	rel := &release.Release{
		Title:    "Test Movie 2024 1080p",
		Protocol: "torrent",
		InfoHash: e2eTestHash,
		Magnet:   "magnet:?xt=urn:btih:" + e2eTestHash + "&dn=test",
		Indexer:  "TestTracker",
	}
	slot := formatStreamSlotPath("default", "movie", "tt0000001", 0)
	if _, _, err := sessMgr.CreateDeferredSession(slot, rel, &session.ContentMeta{ImdbID: "tt0000001"}, "movie", "tt0000001", "Test Movie", "default"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Full-file request: the torrent is added to qBittorrent and served from disk.
	req := httptest.NewRequest(http.MethodGet, "/play/"+slot, nil)
	rec := httptest.NewRecorder()
	srv.handlePlay(rec, req, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("play status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Len(); got != len(content) {
		t.Fatalf("served %d bytes, want %d", got, len(content))
	}
	if rec.Body.String() != string(content) {
		t.Fatalf("served bytes do not match file content")
	}

	// Range request: seeking must return exactly the requested window.
	req = httptest.NewRequest(http.MethodGet, "/play/"+slot, nil)
	req.Header.Set("Range", "bytes=1000000-1000999")
	rec = httptest.NewRecorder()
	srv.handlePlay(rec, req, nil)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rec.Code)
	}
	if rec.Body.String() != string(content[1000000:1001000]) {
		t.Fatalf("range bytes do not match file content")
	}

	// Session bookkeeping: playback validated and slot committed for /next.
	sess, err := sessMgr.GetSession(slot)
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	if !sess.HasPreviouslyServed() {
		t.Fatalf("expected session to be marked playback-validated")
	}
	if _, committed := srv.recordedSuccessSessionIDs.Load(slot); !committed {
		t.Fatalf("expected slot to be committed for /next cursor")
	}
}

func TestTorrentPlayRejectsNonTorrentSession(t *testing.T) {
	saveDir := t.TempDir()
	qbit := newMockQBittorrent(t, saveDir, "x.mkv", 1, true)
	srv, sessMgr := newTorrentE2EServer(t, qbit.URL, saveDir)

	rel := &release.Release{Title: "Not a torrent", Link: "https://tracker/get?id=1"}
	slot := formatStreamSlotPath("default", "movie", "tt0000002", 0)
	if _, _, err := sessMgr.CreateDeferredSession(slot, rel, nil, "movie", "tt0000002", "Movie", "default"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/play/"+slot, nil)
	rec := httptest.NewRecorder()
	srv.handlePlay(rec, req, nil)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected non-torrent session to fail with 504, got %d", rec.Code)
	}
	if !srv.sessionManager.GetSlotFailedDuringPlayback(slot) {
		t.Fatalf("expected failed slot to be marked")
	}
}
