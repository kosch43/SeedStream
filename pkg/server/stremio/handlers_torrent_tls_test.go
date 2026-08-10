package stremio

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"seedstream/pkg/release"
	"seedstream/pkg/search/triage"
	"seedstream/pkg/session"
)

// TestTorrentStreamServedOverHTTPS proves a torrent stream is delivered inside
// a TLS session — both a full read and a range request — and that the server
// actually saw an encrypted connection rather than falling back to plaintext.
func TestTorrentStreamServedOverHTTPS(t *testing.T) {
	saveDir := t.TempDir()
	fileName := "Encrypted.Movie.2024.1080p.mkv"
	content := make([]byte, 1536*1024)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(saveDir, fileName), content, 0o644); err != nil {
		t.Fatalf("write test video: %v", err)
	}

	qbit := newMockQBittorrent(t, saveDir, fileName, int64(len(content)), true)
	srv, sessMgr := newTorrentE2EServer(t, qbit.URL, saveDir)

	rel := &release.Release{
		Title:    "Encrypted Movie 2024 1080p",
		Protocol: "torrent",
		InfoHash: e2eTestHash,
		Magnet:   "magnet:?xt=urn:btih:" + e2eTestHash,
		Indexer:  "TestTracker",
	}
	slot := formatStreamSlotPath("default", "movie", "tt0000009", 0)
	if _, _, err := sessMgr.CreateDeferredSession(slot, rel, &session.ContentMeta{ImdbID: "tt0000009"}, "movie", "tt0000009", "Encrypted Movie", "default"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Record whether the request reached the handler over TLS.
	var sawTLS bool
	var negotiatedVersion uint16
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			sawTLS = true
			negotiatedVersion = r.TLS.Version
		}
		srv.handlePlay(w, r, nil)
	}))
	defer ts.Close()

	if ts.URL[:8] != "https://" {
		t.Fatalf("test server is not HTTPS: %s", ts.URL)
	}
	client := ts.Client() // trusts the server's generated cert

	// Full read over TLS.
	resp, err := client.Get(ts.URL + "/play/" + slot)
	if err != nil {
		t.Fatalf("https GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("https play status = %d, body: %s", resp.StatusCode, string(body))
	}
	if len(body) != len(content) {
		t.Fatalf("https served %d bytes, want %d", len(body), len(content))
	}
	if string(body) != string(content) {
		t.Fatalf("https served bytes do not match the file on disk")
	}

	// Range request over TLS — seeking must work through the encrypted channel.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/play/"+slot, nil)
	req.Header.Set("Range", "bytes=500000-500999")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("https range GET: %v", err)
	}
	rangeBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("https range status = %d, want 206", resp.StatusCode)
	}
	if string(rangeBody) != string(content[500000:501000]) {
		t.Fatalf("https range bytes do not match the file on disk")
	}

	if !sawTLS {
		t.Fatalf("handler did not see a TLS connection — stream was served in plaintext")
	}
	if negotiatedVersion < tls.VersionTLS12 {
		t.Fatalf("negotiated TLS version 0x%04x is below TLS 1.2", negotiatedVersion)
	}
}

// TestStreamURLsInheritBaseURLScheme locks in that stream URLs handed to
// Stremio follow the configured base URL, so an https base URL yields https
// stream links rather than silently downgrading to http.
func TestStreamURLsInheritBaseURLScheme(t *testing.T) {
	srv := &Server{baseURL: "https://seedstream.example.com"}
	base := srv.baseURLWithToken(nil, nil)
	if base != "https://seedstream.example.com" {
		t.Fatalf("baseURLWithToken = %q", base)
	}

	key := StreamSlotKey{StreamID: "default", ContentType: "movie", ID: "tt0000010"}
	list := &playlistResult{
		Params: &SearchParams{ContentTitle: "Example"},
		Candidates: []triage.Candidate{
			{Release: &release.Release{Title: "Example 2024 1080p", Protocol: "torrent"}},
			{Release: &release.Release{Title: "Example 2024 2160p", Protocol: "torrent"}},
		},
	}

	streams := buildStreamsFromPlaylist(list, key, "SeedStream", base, true)
	if len(streams) == 0 {
		t.Fatalf("expected stream entries")
	}
	for _, s := range streams {
		if got := s.URL; len(got) < 8 || got[:8] != "https://" {
			t.Fatalf("stream URL is not https: %q", got)
		}
	}
}
