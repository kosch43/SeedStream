package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"seedstream/pkg/core/config"
)

func TestValidateConfigRejectsUnresolvedProwlarrIndexerPlaceholder(t *testing.T) {
	enabled := true
	s := &Server{}

	errs := s.validateConfig(&config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{{
			Enabled: &enabled,
			Name:    "Prowlarr",
			URL:     "http://[::1",
			APIPath: "{indexer_id}/api",
			Type:    "aggregator",
		}},
	})

	if got := errs["indexers.0.api_path"]; got == "" {
		t.Fatalf("expected api_path validation error, got %#v", errs)
	}
	if got := errs["indexers.0.url"]; got != "" {
		t.Fatalf("expected placeholder validation to stop ping before url validation, got url error %q", got)
	}
}

func TestValidateConfigWithPlanIgnoresUnchangedInvalidIndexerDuringEdit(t *testing.T) {
	enabled := true
	disabled := false
	s := &Server{}

	current := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
			{Enabled: &disabled, Name: "Valid", URL: "https://indexer.example"},
		},
	}
	next := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
			{Enabled: &disabled, Name: "Updated", URL: "https://indexer.example"},
		},
	}
	body, err := json.Marshal(map[string]interface{}{"indexers": next.Indexers})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	errs := s.validateConfigWithPlan(next, validationPlanFromPatch(body, current, next))
	if got := errs["indexers.0.url"]; got != "" {
		t.Fatalf("expected unchanged invalid indexer to be ignored during unrelated edit, got %q", got)
	}
}

func TestValidateConfigWithPlanAllowsIndexerDeleteDespiteOtherInvalidIndexer(t *testing.T) {
	enabled := true
	disabled := false
	s := &Server{}

	current := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
			{Enabled: &disabled, Name: "Valid", URL: "https://indexer.example"},
		},
	}
	next := &config.Config{
		KeepLogFiles: 9,
		Indexers: []config.IndexerConfig{
			{Enabled: &enabled, Name: "Broken", Type: "aggregator"},
		},
	}
	body, err := json.Marshal(map[string]interface{}{"indexers": next.Indexers})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	errs := s.validateConfigWithPlan(next, validationPlanFromPatch(body, current, next))
	if got := errs["indexers.0.url"]; got != "" {
		t.Fatalf("expected indexer delete to skip unrelated indexer validation, got %q", got)
	}
}

func TestValidateConfigRejectsOutOfRangePlaybackStartupTimeout(t *testing.T) {
	s := &Server{}

	errs := s.validateConfig(&config.Config{
		KeepLogFiles:                  9,
		PlaybackStartupTimeoutSeconds: 0,
	})

	if got := errs["playback_startup_timeout_seconds"]; got == "" {
		t.Fatalf("expected playback startup timeout validation error, got %#v", errs)
	}
}

func TestValidateConfigRejectsInvalidGlobalIndexerProxyURL(t *testing.T) {
	s := &Server{}

	errs := s.validateConfig(&config.Config{
		KeepLogFiles:                  9,
		PlaybackStartupTimeoutSeconds: 5,
		IndexerProxyURL:               "socks5://127.0.0.1:1080",
	})

	if got := errs["indexer_proxy_url"]; got == "" {
		t.Fatalf("expected global indexer proxy validation error, got %#v", errs)
	}
}

func TestValidateConfigRejectsUnreachableGlobalIndexerProxyURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := &Server{}
	enabled := true
	errs := s.validateConfig(&config.Config{
		KeepLogFiles:                  9,
		PlaybackStartupTimeoutSeconds: 5,
		IndexerProxyURL:               "http://" + addr,
		Indexers: []config.IndexerConfig{{
			Enabled: &enabled,
			Name:    "BrokenIndexer",
			URL:     "http://example.invalid",
			APIPath: "/api",
			APIKey:  "abc",
			Type:    "torznab",
		}},
	})

	if got := errs["indexer_proxy_url"]; got == "" {
		t.Fatalf("expected global indexer proxy reachability error, got %#v", errs)
	}
}

func TestValidateConfigWithPlanAllowsLegacyOriginalIDTitleLanguage(t *testing.T) {
	s := &Server{}

	cfg := &config.Config{
		MovieSearchQueries: []config.SearchQueryConfig{{
			Name:                "MovieQuery01",
			SearchMode:          "id",
			SearchTitleLanguage: "original",
		}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateMovieSearchQueries: true})
	if got := errs["movie_search_queries.0.search_title_languages"]; got != "" {
		t.Fatalf("expected legacy original title language to be accepted, got %q", got)
	}
}

func TestValidateConfigWithPlanGlobalProxyPassesWhenAnyIndexerReachable(t *testing.T) {
	enabled := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "ok.indexer" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	s := &Server{}
	cfg := &config.Config{
		IndexerProxyURL: proxy.URL,
		Indexers: []config.IndexerConfig{
			{
				Enabled: &enabled,
				Name:    "Failing",
				URL:     "http://fail.indexer",
				APIPath: "/api",
				APIKey:  "abc",
				Type:    "torznab",
			},
			{
				Enabled: &enabled,
				Name:    "Healthy",
				URL:     "http://ok.indexer",
				APIPath: "/api",
				APIKey:  "abc",
				Type:    "torznab",
			},
		},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateIndexerProxyURL: true})
	if got := errs["indexer_proxy_url"]; got != "" {
		t.Fatalf("expected global proxy verification to pass when one indexer is reachable, got %q", got)
	}
}

func TestValidateConfigWithPlanGlobalProxyFailsWhenNoIndexerReachable(t *testing.T) {
	enabled := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	s := &Server{}
	cfg := &config.Config{
		IndexerProxyURL: proxy.URL,
		Indexers: []config.IndexerConfig{{
			Enabled: &enabled,
			Name:    "Failing",
			URL:     "http://fail.indexer",
			APIPath: "/api",
			APIKey:  "abc",
			Type:    "torznab",
		}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateIndexerProxyURL: true})
	got := errs["indexer_proxy_url"]
	if got == "" {
		t.Fatalf("expected global proxy verification error, got %#v", errs)
	}
	if !strings.Contains(got, "could not reach any enabled indexer") {
		t.Fatalf("expected aggregate global proxy error, got %q", got)
	}
}

func TestValidateConfigWithPlanIndexerProxyChecksIndexerConnection(t *testing.T) {
	enabled := true
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()

	s := &Server{}
	cfg := &config.Config{
		Indexers: []config.IndexerConfig{{
			Enabled:  &enabled,
			Name:     "Failing",
			URL:      "http://blocked.indexer",
			APIPath:  "/api",
			APIKey:   "abc",
			Type:     "newznab",
			ProxyURL: proxy.URL,
		}},
	}

	errs := s.validateConfigWithPlan(cfg, configValidationPlan{validateIndexers: true})
	if got := errs["indexers.0.url"]; got == "" {
		t.Fatalf("expected indexer connectivity error, got %#v", errs)
	}
	if got := errs["indexers.0.proxy_url"]; got != "" {
		t.Fatalf("expected no standalone proxy reachability error, got %q", got)
	}
}

// TestValidateConfigWithPlanRejectsUnbootableTLS locks in that a certificate
// which would stop the server from starting is rejected at save time. Startup
// treats a bad certificate as fatal, so allowing the save would leave the next
// restart crash-looping with no way in through the UI to correct it.
func TestValidateConfigWithPlanRejectsUnbootableTLS(t *testing.T) {
	s := &Server{}

	t.Run("enabled with no certificate", func(t *testing.T) {
		cfg := &config.Config{TLSEnabled: true}
		body, err := json.Marshal(map[string]any{"tls_enabled": true})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		plan := validationPlanFromPatch(body, &config.Config{}, cfg)
		if !plan.validateTLS {
			t.Fatalf("expected the patch to trigger TLS validation")
		}
		errs := s.validateConfigWithPlan(cfg, plan)
		if _, ok := errs["tls_enabled"]; !ok {
			t.Fatalf("expected a tls_enabled error, got %#v", errs)
		}
	})

	t.Run("unreadable certificate path", func(t *testing.T) {
		cfg := &config.Config{
			TLSEnabled:  true,
			TLSCertFile: filepath.Join(t.TempDir(), "missing.pem"),
			TLSKeyFile:  filepath.Join(t.TempDir(), "missing.key"),
		}
		body, _ := json.Marshal(map[string]any{"tls_cert_file": cfg.TLSCertFile})
		plan := validationPlanFromPatch(body, &config.Config{}, cfg)
		errs := s.validateConfigWithPlan(cfg, plan)
		if _, ok := errs["tls_cert_file"]; !ok {
			t.Fatalf("expected a tls_cert_file error, got %#v", errs)
		}
	})

	t.Run("bad auto-cert domain", func(t *testing.T) {
		cfg := &config.Config{TLSEnabled: true, TLSAutoDomain: "not-a-domain"}
		body, _ := json.Marshal(map[string]any{"tls_auto_domain": cfg.TLSAutoDomain})
		plan := validationPlanFromPatch(body, &config.Config{}, cfg)
		errs := s.validateConfigWithPlan(cfg, plan)
		if _, ok := errs["tls_auto_domain"]; !ok {
			t.Fatalf("expected a tls_auto_domain error, got %#v", errs)
		}
	})

	t.Run("HTTPS off saves cleanly", func(t *testing.T) {
		cfg := &config.Config{TLSEnabled: false, TLSCertFile: "/nope.pem"}
		body, _ := json.Marshal(map[string]any{"tls_enabled": false})
		plan := validationPlanFromPatch(body, &config.Config{}, cfg)
		errs := s.validateConfigWithPlan(cfg, plan)
		for k := range errs {
			if len(k) >= 4 && k[:4] == "tls_" {
				t.Fatalf("did not expect TLS errors when HTTPS is off, got %#v", errs)
			}
		}
	})
}
