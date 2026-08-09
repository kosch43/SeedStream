package cardigann

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"seedstream/pkg/core/config"
)

// fakeTracker is a stand-in private tracker: it requires a form login carrying a
// CSRF token, sets a session cookie, and serves a results table.
// fakeTracker is shared by every test in this package, and its handlers run on
// the httptest server's own goroutines — one per connection. A test that issues
// concurrent searches therefore writes these fields from several goroutines at
// once, so they are guarded rather than bare.
type fakeTracker struct {
	mu         sync.Mutex
	loginHits  int
	searchHits int
	lastQuery  string
	lastCats   []string
}

func (f *fakeTracker) logins() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginHits
}

func (f *fakeTracker) searches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searchHits
}

func (f *fakeTracker) query() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastQuery
}

func (f *fakeTracker) cats() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lastCats...)
}

func (f *fakeTracker) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/login.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<html><body>
				<form id="login" method="post" action="/login.php">
					<input name="username"><input name="password" type="password">
					<input name="csrf" value="tok-12345" type="hidden">
				</form></body></html>`)
			return
		}
		f.mu.Lock()
		f.loginHits++
		f.mu.Unlock()
		_ = r.ParseForm()
		if r.FormValue("username") != "alice" || r.FormValue("password") != "hunter2" {
			fmt.Fprint(w, `<html><body><span class="error">Invalid credentials</span></body></html>`)
			return
		}
		if r.FormValue("csrf") != "tok-12345" {
			fmt.Fprint(w, `<html><body><span class="error">Missing CSRF token</span></body></html>`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a></body></html>`)
	})

	mux.HandleFunc("/browse.php", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err != nil || c.Value != "ok" {
			http.Error(w, "not logged in", http.StatusForbidden)
			return
		}
		f.mu.Lock()
		f.searchHits++
		f.lastQuery = r.URL.Query().Get("search")
		f.lastCats = r.URL.Query()["cat[]"]
		f.mu.Unlock()
		fmt.Fprint(w, `<html><body><table class="torrents"><tbody>
			<tr>
				<td><a href="details.php?id=101">Some.Movie.2021.2160p.BluRay.REMUX-GRP</a></td>
				<td><a href="download.php?id=101">dl</a></td>
				<td>2021-05-04 11:22:33</td>
				<td>42.5 GB</td>
				<td>37</td>
				<td>4</td>
			</tr>
			<tr>
				<td><a href="details.php?id=102">Some.Show.S05E01.1080p.WEB-DL-GRP</a></td>
				<td><a href="download.php?id=102">dl</a></td>
				<td>2022-01-02 03:04:05</td>
				<td>2.3 GiB</td>
				<td>0</td>
				<td>9</td>
			</tr>
		</tbody></table></body></html>`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const testDefinition = `
id: faketracker
name: Fake Tracker
type: private
links:
  - %s
caps:
  categorymappings:
    - {id: "1", cat: Movies}
    - {id: "2", cat: TV}
  modes:
    search: [q]
settings:
  - name: username
    type: text
    label: Username
  - name: password
    type: password
    label: Password
login:
  path: login.php
  method: form
  form: form#login
  inputs:
    username: "{{ .Config.username }}"
    password: "{{ .Config.password }}"
  error:
    - selector: span.error
search:
  paths:
    - path: browse.php
  inputs:
    $raw: "{{ range .Categories }}cat[]={{.}}&{{end}}"
    search: "{{ .Keywords }}"
  rows:
    selector: table.torrents > tbody > tr
  fields:
    title:
      selector: a[href*="details.php?id="]
    details:
      selector: a[href*="details.php?id="]
      attribute: href
    download:
      selector: a[href*="download.php"]
      attribute: href
    date:
      selector: td:nth-child(3)
      filters:
        - name: dateparse
          args: "2006-01-02 15:04:05"
    size:
      selector: td:nth-child(4)
    seeders:
      selector: td:nth-child(5)
    leechers:
      selector: td:nth-child(6)
`

func newTestEngine(t *testing.T, base string, settings map[string]string) *Engine {
	t.Helper()
	def, err := Parse([]byte(fmt.Sprintf(testDefinition, base)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	eng, err := NewEngine(def, "", settings, 10*time.Second)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

// TestEngineLogsInAndScrapes is the end-to-end proof that a definition alone is
// enough to drive a tracker: log in with a CSRF-protected form, run a search,
// and turn the resulting HTML into releases.
func TestEngineLogsInAndScrapes(t *testing.T) {
	f := &fakeTracker{}
	srv := f.server(t)
	eng := newTestEngine(t, srv.URL, map[string]string{"username": "alice", "password": "hunter2"})

	results, err := eng.Search(context.Background(), "some movie", []string{"1"}, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if f.logins() != 1 {
		t.Fatalf("expected exactly one login, got %d", f.logins())
	}
	if f.query() != "some movie" {
		t.Fatalf("keywords not passed through: %q", f.query())
	}
	if cats := f.cats(); len(cats) != 1 || cats[0] != "1" {
		t.Fatalf("categories not expanded into repeated params: %v", f.cats())
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(results))
	}

	first := results[0]
	if first.Title != "Some.Movie.2021.2160p.BluRay.REMUX-GRP" {
		t.Fatalf("title wrong: %q", first.Title)
	}
	gib := float64(int64(1) << 30)
	wantSize := int64(42.5 * gib)
	if first.Size != wantSize {
		t.Fatalf("size parsed wrong: got %d want %d", first.Size, wantSize)
	}
	if first.Seeders != 37 || first.Leechers != 4 {
		t.Fatalf("swarm counts wrong: %d/%d", first.Seeders, first.Leechers)
	}
	if !strings.HasPrefix(first.Download, srv.URL) || !strings.Contains(first.Download, "download.php?id=101") {
		t.Fatalf("download link not resolved to absolute: %q", first.Download)
	}
	if !strings.Contains(first.PublishDate, "2021") {
		t.Fatalf("date not parsed: %q", first.PublishDate)
	}
	wantGiB := int64(2.3 * gib)
	if results[1].Size != wantGiB {
		t.Fatalf("GiB size parsed wrong: got %d want %d", results[1].Size, wantGiB)
	}
}

// TestEngineReusesSession: a second search must not log in again.
func TestEngineReusesSession(t *testing.T) {
	f := &fakeTracker{}
	srv := f.server(t)
	eng := newTestEngine(t, srv.URL, map[string]string{"username": "alice", "password": "hunter2"})

	for i := 0; i < 3; i++ {
		if _, err := eng.Search(context.Background(), "x", nil, nil); err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
	}
	if f.logins() != 1 {
		t.Fatalf("session not reused: %d logins for 3 searches", f.logins())
	}
	if f.searches() != 3 {
		t.Fatalf("expected 3 searches, got %d", f.searches())
	}
}

// TestEngineReportsBadCredentials surfaces the tracker's own error rather than a
// generic failure, so the user knows to fix their password.
func TestEngineReportsBadCredentials(t *testing.T) {
	f := &fakeTracker{}
	srv := f.server(t)
	eng := newTestEngine(t, srv.URL, map[string]string{"username": "alice", "password": "wrong"})

	_, err := eng.Search(context.Background(), "x", nil, nil)
	if err == nil {
		t.Fatal("bad credentials must fail the search")
	}
	if !strings.Contains(err.Error(), "Invalid credentials") {
		t.Fatalf("error should carry the tracker's message, got: %v", err)
	}
}

// TestBaseURLOverride is what keeps a definition usable when a tracker moves to
// a new domain and the shipped file still points at the old one.
func TestBaseURLOverride(t *testing.T) {
	f := &fakeTracker{}
	srv := f.server(t)
	def, err := Parse([]byte(fmt.Sprintf(testDefinition, "https://old-domain.invalid/")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	eng, err := NewEngine(def, srv.URL, map[string]string{"username": "alice", "password": "hunter2"}, 10*time.Second)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	results, err := eng.Search(context.Background(), "x", nil, nil)
	if err != nil {
		t.Fatalf("override should redirect all requests to the new host: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected results from the overridden host, got %d", len(results))
	}
}

func TestEngineUsesVerifiedTLSAndPerIndexerCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>ok</body></html>")
	}))
	defer server.Close()

	def, err := Parse([]byte(fmt.Sprintf(testDefinition, server.URL)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	withoutCA, err := NewEngine(def, "", nil, 10*time.Second)
	if err != nil {
		t.Fatalf("engine without CA: %v", err)
	}
	if tlsConfig := withoutCA.http.Transport.(*http.Transport).TLSClientConfig; tlsConfig.InsecureSkipVerify {
		t.Fatal("Cardigann must not disable TLS verification")
	}
	if _, _, err := withoutCA.do(context.Background(), http.MethodGet, server.URL, nil, nil); err == nil {
		t.Fatal("self-signed HTTPS tracker must fail without a custom CA")
	}

	caPath := filepath.Join(t.TempDir(), "tracker-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	withCA, err := NewEngine(def, "", nil, 10*time.Second, config.IndexerConfig{TLSCAFile: caPath})
	if err != nil {
		t.Fatalf("engine with CA: %v", err)
	}
	if _, _, err := withCA.do(context.Background(), http.MethodGet, server.URL, nil, nil); err != nil {
		t.Fatalf("custom CA should trust tracker: %v", err)
	}
	if roots := withCA.http.Transport.(*http.Transport).TLSClientConfig.RootCAs; roots == nil {
		t.Fatal("custom CA should configure a root pool")
	}
	if _, err := x509.SystemCertPool(); err != nil {
		t.Logf("system certificate pool unavailable in test environment: %v", err)
	}
}

func TestEnginePreservesConfiguredProxy(t *testing.T) {
	var proxiedHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxiedHost = r.URL.Host
		fmt.Fprint(w, "<html><body>proxied</body></html>")
	}))
	defer proxy.Close()

	def := &Definition{ID: "proxy", Name: "Proxy", Links: []string{"http://tracker.invalid/"}}
	eng, err := NewEngine(def, "", nil, 10*time.Second, config.IndexerConfig{ProxyURL: proxy.URL})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if _, _, err := eng.do(context.Background(), http.MethodGet, "http://tracker.invalid/search", nil, nil); err != nil {
		t.Fatalf("proxied request: %v", err)
	}
	if proxiedHost != "tracker.invalid" {
		t.Fatalf("proxy saw host %q, want tracker.invalid", proxiedHost)
	}
}
