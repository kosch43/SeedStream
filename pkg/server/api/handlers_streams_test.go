package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
)

func testAPIStream() *auth.Stream {
	stream := &auth.Stream{
		Username:          "alice",
		Token:             "stream-token",
		IndexerMode:       "combine",
		IndexerOverrides:  map[string]config.IndexerSearchConfig{"tracker": {SearchResultLimit: 10}},
		IndexerSelections: []string{"tracker"},
		TorrentClient: &config.TorrentClientConfig{
			Name:     "box",
			URL:      "https://torrent-user:torrent-password@box.example",
			Username: "torrent-user",
			Password: "torrent-password",
		},
		ProwlarrURL:        "https://prowlarr-user:prowlarr-password@prowlarr.example?apikey=prowlarr-query-secret",
		ProwlarrAPIKey:     "prowlarr-secret",
		PasswordHash:       "password-hash",
		MustChangePassword: true,
	}
	return stream
}

func TestStreamAPIResponseRedactsCredentials(t *testing.T) {
	s := &Server{config: &config.Config{AddonBaseURL: "https://seedstream.example/addon"}}
	response := s.streamAPIResponse(testAPIStream())
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	body := string(raw)
	for _, secret := range []string{"password-hash", "prowlarr-secret", "prowlarr-query-secret", "torrent-user", "torrent-password", "prowlarr-user", "prowlarr-password"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response contains secret %q: %s", secret, body)
		}
	}
	for _, field := range []string{"password_hash", "must_change_password", "prowlarr_api_key"} {
		if strings.Contains(body, `"`+field+`"`) {
			t.Fatalf("response contains hidden field %q: %s", field, body)
		}
	}
	if response.Token != "stream-token" {
		t.Fatalf("token = %q, want stream-token", response.Token)
	}
	if response.ManifestURL != "https://seedstream.example/addon/stream-token/manifest.json" {
		t.Fatalf("manifest URL = %q", response.ManifestURL)
	}
	if response.TorrentClient == nil || response.TorrentClient.URL != "https://box.example" {
		t.Fatalf("torrent client was not safely redacted: %#v", response.TorrentClient)
	}
	if response.ProwlarrURL != "https://prowlarr.example" {
		t.Fatalf("Prowlarr URL was not safely redacted: %q", response.ProwlarrURL)
	}
}

func TestStreamHandlersRequireAdminContext(t *testing.T) {
	s := &Server{config: &config.Config{AdminUsername: "admin"}}
	req := httptest.NewRequest(http.MethodGet, "/api/streams", nil)
	req = req.WithContext(auth.ContextWithStream(req.Context(), &auth.Stream{Username: "alice"}))
	rr := httptest.NewRecorder()
	s.handleStreamsList(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestStreamHandlersUseConfiguredAdminUsername(t *testing.T) {
	s := &Server{config: &config.Config{AdminUsername: "Admin"}}
	req := httptest.NewRequest(http.MethodGet, "/api/streams", nil)
	req = req.WithContext(auth.ContextWithAdmin(req.Context(), "Admin", &auth.Stream{Username: "Admin"}))
	rr := httptest.NewRecorder()
	s.handleStreamsList(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}
