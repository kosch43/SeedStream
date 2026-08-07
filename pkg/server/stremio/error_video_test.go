package stremio

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// withErrorVideoDir points the override lookup at a temp directory. The
// directory is resolved through a sync.Once, so tests set it directly rather
// than fighting the singleton.
func withErrorVideoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	errorVideoOnce.Do(func() {}) // consume the Once so it will not overwrite
	prev := errorVideoDir
	errorVideoDir = dir
	t.Cleanup(func() { errorVideoDir = prev })
	return dir
}

// bundledStub stands in for the embedded clip.
func bundledStub(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		io.WriteString(w, body)
	})
}

// TestBundledClipServedWhenNoOverride keeps the default behaviour: without a
// replacement, the clip shipped in the image is what plays.
func TestBundledClipServedWhenNoOverride(t *testing.T) {
	withErrorVideoDir(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, errorVideoPath, nil)
	serveErrorVideo(rec, req, bundledStub("bundled"))

	if got := rec.Body.String(); got != "bundled" {
		t.Fatalf("expected the bundled clip, got %q", got)
	}
}

// TestOverrideWins is the point of the change: a clip dropped into the data
// directory replaces the bundled one, without rebuilding the image. The data
// directory is the only thing that survives a rebuild, so editing the copy
// inside the image would be undone by the next build.
func TestOverrideWins(t *testing.T) {
	dir := withErrorVideoDir(t)
	if err := os.WriteFile(filepath.Join(dir, "failure.mp4"), []byte("custom clip"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, errorVideoPath, nil)
	serveErrorVideo(rec, req, bundledStub("bundled"))

	if got := rec.Body.String(); got != "custom clip" {
		t.Fatalf("the operator's clip must win, got %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("content type %q, want video/mp4", got)
	}
}

// TestOverrideAcceptsOtherContainers: re-encoding a clip only to change its
// container is wasted work, so mkv and webm are served too, each with the type
// that matches rather than whatever the request path claimed.
func TestOverrideAcceptsOtherContainers(t *testing.T) {
	for name, wantType := range map[string]string{
		"failure.mkv":  "video/x-matroska",
		"failure.webm": "video/webm",
	} {
		dir := withErrorVideoDir(t)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("clip"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		serveErrorVideo(rec, httptest.NewRequest(http.MethodGet, errorVideoPath, nil), bundledStub("bundled"))
		if got := rec.Body.String(); got != "clip" {
			t.Errorf("%s: expected the custom clip, got %q", name, got)
		}
		if got := rec.Header().Get("Content-Type"); got != wantType {
			t.Errorf("%s: content type %q, want %q", name, got, wantType)
		}
	}
}

// TestEmptyOverrideIsIgnored: a zero-length file is a failed copy, not a clip.
// Serving it would produce a player that fails silently with no explanation,
// which is worse than the default.
func TestEmptyOverrideIsIgnored(t *testing.T) {
	dir := withErrorVideoDir(t)
	if err := os.WriteFile(filepath.Join(dir, "failure.mp4"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	serveErrorVideo(rec, httptest.NewRequest(http.MethodGet, errorVideoPath, nil), bundledStub("bundled"))
	if got := rec.Body.String(); got != "bundled" {
		t.Fatalf("an empty file is a failed copy, not a clip; expected the bundled one, got %q", got)
	}
}

// TestErrorVideoIsNeverCached: a cached failure clip keeps playing for a title
// that has since started working, which reads as SeedStream being broken rather
// than as one attempt having failed.
func TestErrorVideoIsNeverCached(t *testing.T) {
	withErrorVideoDir(t)
	rec := httptest.NewRecorder()
	serveErrorVideo(rec, httptest.NewRequest(http.MethodGet, errorVideoPath, nil), bundledStub("bundled"))

	if got := rec.Header().Get("Cache-Control"); got == "" {
		t.Fatal("the failure clip must not be cacheable")
	}
}

// TestOverrideSupportsRangeRequests: players seek. A range request answered with
// the whole file and a 200 is a broken response, and http.ServeContent is what
// gets this right.
func TestOverrideSupportsRangeRequests(t *testing.T) {
	dir := withErrorVideoDir(t)
	if err := os.WriteFile(filepath.Join(dir, "failure.mp4"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, errorVideoPath, nil)
	req.Header.Set("Range", "bytes=2-4")
	rec := httptest.NewRecorder()
	serveErrorVideo(rec, req, bundledStub("bundled"))

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "234" {
		t.Fatalf("body %q, want %q", got, "234")
	}
}

// TestStatusNamesWhereToPutIt: the whole point of reporting this is telling an
// operator where a replacement goes, so the path must be there whether or not
// one exists yet.
func TestStatusNamesWhereToPutIt(t *testing.T) {
	dir := withErrorVideoDir(t)
	s := &Server{}

	got := s.ErrorVideoStatus()
	if got.Custom {
		t.Error("no clip has been dropped in, so none is custom")
	}
	if got.Path != dir {
		t.Errorf("path %q, want the directory %q", got.Path, dir)
	}
	if len(got.Names) == 0 {
		t.Error("the accepted filenames must be reported")
	}

	if err := os.WriteFile(filepath.Join(dir, "failure.mp4"), []byte("clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = s.ErrorVideoStatus()
	if !got.Custom || got.Size != 4 {
		t.Fatalf("expected the custom clip to be reported, got %+v", got)
	}
}
