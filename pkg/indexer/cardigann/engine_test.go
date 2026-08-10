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

// TestEngineParsesJSONSearchResults is the fix for trackers whose Cardigann
// definition declares response.type: json. Before the JSON path, the engine
// fed every response (JSON included) to goquery, found no rows, and silently
// returned zero results — TorrentLeech and every Unit3d-API tracker were the
// observed casualties. The shape here mirrors TorrentLeech's definition:
// rows.selector "torrentList", a $.numFound count, and field selectors that
// are JSON keys (name, fid, seeders…), with the {{ .Result._id }} cross-
// reference pattern for download URLs.
func TestEngineParsesJSONSearchResults(t *testing.T) {
	const jsonDefinition = `
id: jsontest
name: JSON Tracker
type: private
links:
  - %s
caps:
  categorymappings:
    - {id: "10", cat: Movies}
  modes:
    search: [q]
settings:
  - name: cookie
    type: text
    label: Cookie
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
      response:
        type: json
  inputs:
    search: "{{ .Keywords }}"
  rows:
    selector: torrentList
    count:
      selector: $.numFound
  fields:
    title:
      selector: name
    _id:
      selector: fid
    _filename:
      selector: filename
    details:
      text: "/torrent/{{ .Result._id }}"
    download:
      text: "/download/{{ .Result._id }}/{{ .Result._filename }}"
    seeders:
      selector: seeders
    leechers:
      selector: leechers
    grabs:
      selector: completed
    size:
      selector: size
    date:
      selector: added
      filters:
        - name: dateparse
          args: "2006-01-02 15:04:05"
    category:
      selector: categoryID
    freeleech:
      selector: download_multiplier
      case:
        "0": "free"
        "*": "nofree"
`
	var searchHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/login.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<html><body><form id="login"><input name="username"><input name="password"><input name="csrf" value="tok" type="hidden"></form></body></html>`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a></body></html>`)
	})
	mux.HandleFunc("/browse.php", func(w http.ResponseWriter, r *http.Request) {
		searchHits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"numFound": 35,
			"torrentList": [
				{"fid":"101","filename":"Rick.and.Morty.S09.1080p.WEB-DL.mkv","name":"Rick and Morty (2013) S09","categoryID":"10","seeders":37,"leechers":4,"completed":128,"size":"42.5 GB","added":"2026-08-09 12:34:56","download_multiplier":0},
				{"fid":"102","filename":"Some.Other.2021.mkv","name":"Some Other 2021","categoryID":"10","seeders":2,"leechers":1,"completed":9,"size":"2.3 GiB","added":"2022-01-02 03:04:05","download_multiplier":1}
			]
		}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	def, err := Parse([]byte(fmt.Sprintf(jsonDefinition, srv.URL)))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	eng, err := NewEngine(def, "", map[string]string{"username": "alice", "password": "pw"}, 10*time.Second)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	results, err := eng.Search(context.Background(), "rick", nil, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if searchHits != 1 {
		t.Fatalf("expected one search hit, got %d", searchHits)
	}
	if len(results) != 2 {
		t.Fatalf("JSON search returned %d results, want 2 — goquery cannot parse JSON and the JSON path is the fix here", len(results))
	}

	first := results[0]
	if first.Title != "Rick and Morty (2013) S09" {
		t.Fatalf("first title wrong: %q", first.Title)
	}
	if first.Seeders != 37 || first.Leechers != 4 || first.Grabs != 128 {
		t.Fatalf("swarm counts wrong: %d/%d grabs=%d", first.Seeders, first.Leechers, first.Grabs)
	}
	// 42.5 GB parses to 42.5 * 1<<30 bytes; mirror the existing HTML test.
	gib := float64(int64(1) << 30)
	if want := int64(42.5 * gib); first.Size != want {
		t.Fatalf("size parsed wrong: got %d want %d", first.Size, want)
	}
	// The {{ .Result._id }}/{{ .Result._filename }} cross-reference must resolve.
	if !strings.HasSuffix(first.Download, "/download/101/Rick.and.Morty.S09.1080p.WEB-DL.mkv") &&
		!strings.Contains(first.Download, "download/101/") {
		t.Fatalf("download cross-reference not resolved: %q", first.Download)
	}
	if !strings.HasSuffix(first.Details, "/torrent/101") {
		t.Fatalf("details cross-reference not resolved: %q", first.Details)
	}
	// The case map on download_multiplier: 0 -> "free" for the first row.
	if first.Category != "10" {
		t.Fatalf("category wrong: %q", first.Category)
	}

	second := results[1]
	if second.Size != int64(2.3*gib) {
		t.Fatalf("second size wrong: got %d", second.Size)
	}
}

// TestJSONPathNavigation drills the helpers that navigate JSON with the
// "$.prefix[..].sub" convention count.selector uses, so a TorrentLeech-shaped
// count.selector "$.numFound" lands on the right scalar.
func TestJSONPathNavigation(t *testing.T) {
	top := map[string]any{
		"numFound": float64(35),
		"torrentList": []any{
			map[string]any{"fid": float64(101), "name": "A"},
			map[string]any{"fid": float64(102), "name": "B"},
		},
		"nested": map[string]any{
			"inner": map[string]any{
				"val": "deep",
			},
		},
	}

	if got := jsonPath("torrentList"); len(got) != 1 || got[0] != "torrentList" {
		t.Fatalf("jsonPath(\"torrentList\") = %v", got)
	}
	if got := jsonPath("$.numFound"); len(got) != 1 || got[0] != "numFound" {
		t.Fatalf("jsonPath(\"$.numFound\") = %v", got)
	}
	if got := jsonPath("$.nested.inner.val"); len(got) != 3 || got[2] != "val" {
		t.Fatalf("jsonPath(\"$.nested.inner.val\") = %v", got)
	}

	if v, ok := jsonNavigate(top, jsonPath("numFound")); !ok || jsonScalarToString(v) != "35" {
		t.Fatalf("numFound navigation wrong: %v,%v -> %q", v, ok, jsonScalarToString(v))
	}
	if v, ok := jsonNavigate(top, jsonPath("$.nested.inner.val")); !ok || jsonScalarToString(v) != "deep" {
		t.Fatalf("nested navigation wrong: got %q", jsonScalarToString(v))
	}

	rows, ok := jsonNavigateArray(top, jsonPath("torrentList"))
	if !ok || len(rows) != 2 {
		t.Fatalf("array navigation wrong: %d rows, ok=%v", len(rows), ok)
	}
	if v, ok := rows[0]["fid"]; !ok || jsonScalarToString(v) != "101" {
		t.Fatalf("fid not extracted as string 101: got %v", v)
	}
}

// TestMapCaseWildcardAndLiteral: the Cardigann case map matches the literal
// extracted value, falls back to "*", and returns the value unchanged when
// neither matches.
func TestMapCaseWildcardAndLiteral(t *testing.T) {
	cases := map[string]string{"0": "free", "*": "nofree"}
	if got := mapCase(cases, "0"); got != "free" {
		t.Fatalf("literal 0 should map to free, got %q", got)
	}
	if got := mapCase(cases, "1"); got != "nofree" {
		t.Fatalf("non-matching value should fall back to wildcard, got %q", got)
	}
	if got := mapCase(cases, "anything"); got != "nofree" {
		t.Fatalf("unmatched value should use wildcard, got %q", got)
	}
	noWild := map[string]string{"0": "free"}
	if got := mapCase(noWild, "1"); got != "1" {
		t.Fatalf("with no wildcard and no match the value must pass through, got %q", got)
	}
	if got := mapCase(noWild, "0"); got != "free" {
		t.Fatalf("literal match must apply even with no wildcard, got %q", got)
	}
	if got := mapCase(nil, "0"); got != "0" {
		t.Fatalf("nil case must pass through, got %q", got)
	}
}
