package stremio

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"seedstream/pkg/auth"
)

// TestUnsetBaseURLIsDerivedFromTheRequest is the regression test for the
// outage: with addon_base_url empty, stream URLs came out as "/<token>/play/…"
// — relative, so Stremio had no origin to fetch them from, playback never
// started, and the torrent client was never contacted at all.
func TestUnsetBaseURLIsDerivedFromTheRequest(t *testing.T) {
	srv := &Server{listenPort: 7000}
	stream := &auth.Stream{Username: "default", Token: "tok"}

	req := httptest.NewRequest(http.MethodGet, "http://seedstream.local:7000/stream/movie/tt1.json", nil)
	got := srv.baseURLWithToken(req, stream)
	if got != "http://seedstream.local:7000/tok" {
		t.Fatalf("base URL = %q, want it derived from the request host", got)
	}
	if got[0] == '/' {
		t.Fatalf("base URL is relative (%q); no player can fetch that", got)
	}
}

func TestForwardedHeadersWin(t *testing.T) {
	srv := &Server{listenPort: 7000}

	req := httptest.NewRequest(http.MethodGet, "http://10.0.0.4:7000/stream/movie/tt1.json", nil)
	req.Header.Set("X-Forwarded-Host", "seedstream.example, internal.lan")
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := srv.resolveBaseURL(req); got != "https://seedstream.example" {
		t.Errorf("base URL = %q, want the origin the player actually used", got)
	}
}

func TestTLSRequestDerivesHTTPS(t *testing.T) {
	srv := &Server{listenPort: 7000}
	req := httptest.NewRequest(http.MethodGet, "https://secure.example/stream/movie/tt1.json", nil)
	req.TLS = &tls.ConnectionState{}
	if got := srv.resolveBaseURL(req); got != "https://secure.example" {
		t.Errorf("base URL = %q, want https for a TLS request", got)
	}
}

// The configured value always wins: it is the only answer that can be right
// when a proxy rewrites paths, and it is the operator's own statement.
func TestConfiguredBaseURLWinsOverTheRequest(t *testing.T) {
	srv := &Server{baseURL: "https://configured.example/", listenPort: 7000}
	req := httptest.NewRequest(http.MethodGet, "http://something.else/stream/movie/tt1.json", nil)
	if got := srv.resolveBaseURL(req); got != "https://configured.example" {
		t.Errorf("base URL = %q, want the configured value with its trailing slash trimmed", got)
	}
}

// Request-less paths (the programmatic GetStreams entry point) still get an
// absolute URL rather than an empty one.
func TestRequestlessBaseURLFallsBackToTheListenAddress(t *testing.T) {
	srv := &Server{listenPort: 7000}
	if got := srv.resolveBaseURL(nil); got != "http://localhost:7000" {
		t.Errorf("base URL = %q, want the listen address", got)
	}
}
