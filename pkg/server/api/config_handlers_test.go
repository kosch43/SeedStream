package api

import (
	"testing"

	"seedstream/pkg/core/config"
)

func TestConfigForAdminAPIPreservesTrackerCredentials(t *testing.T) {
	cfg := &config.Config{
		AdminPasswordHash: "hash",
		AdminSessionToken: "session-token",
		AdminToken:        "token",
		IndexerProxyURL:   "http://u:p@proxy-global:8080",
		Indexers: []config.IndexerConfig{{
			Name:     "tracker",
			APIKey:   "key",
			Username: "user",
			Password: "pass",
			ProxyURL: "http://u:p@proxy:8888",
		}},
	}

	out := configForAdminAPI(cfg)
	if out.AdminPasswordHash != "" || out.AdminSessionToken != "" || out.AdminToken != "" {
		t.Fatalf("expected admin auth secrets to be cleared, got %#v", out)
	}
	if out.Indexers[0].APIKey != "key" || out.Indexers[0].Username != "user" || out.Indexers[0].Password != "pass" {
		t.Fatalf("expected tracker credentials to remain for admin config reads, got %#v", out.Indexers[0])
	}
	if out.Indexers[0].ProxyURL != "http://u:p@proxy:8888" {
		t.Fatalf("expected full tracker proxy URL for admin config reads, got %q", out.Indexers[0].ProxyURL)
	}
	if out.IndexerProxyURL != "http://u:p@proxy-global:8080" {
		t.Fatalf("expected full global proxy URL for admin config reads, got %q", out.IndexerProxyURL)
	}
}

func TestRedactedConfigForViewerRemovesTrackerCredentials(t *testing.T) {
	cfg := &config.Config{
		IndexerProxyURL: "http://u:p@proxy-global:8080",
		Indexers: []config.IndexerConfig{{
			Name:     "tracker",
			APIKey:   "key",
			Username: "user",
			Password: "pass",
			ProxyURL: "http://u:p@proxy:8888",
		}},
	}

	out := redactedConfigForViewer(cfg)
	if out.Indexers[0].APIKey != "" || out.Indexers[0].Username != "" || out.Indexers[0].Password != "" {
		t.Fatalf("expected tracker credentials to be cleared for viewers, got %#v", out.Indexers[0])
	}
	if out.Indexers[0].ProxyURL != "http://proxy:8888" {
		t.Fatalf("expected proxy userinfo redacted for viewers, got %q", out.Indexers[0].ProxyURL)
	}
	if out.IndexerProxyURL != "http://proxy-global:8080" {
		t.Fatalf("expected global proxy userinfo redacted for viewers, got %q", out.IndexerProxyURL)
	}
}

// TestConfigPatchLeavesAbsentFieldsAlone is the regression test for the save
// that emptied a config. A PUT /api/config body carries only the fields the
// page edited; treating the rest as deletions took config.json from 3647 to
// 2229 bytes in the field, taking the TMDB key, the search queries and the
// addon base URL with it. An absent key means unchanged.
func TestConfigPatchLeavesAbsentFieldsAlone(t *testing.T) {
	current := &config.Config{
		TMDBAPIKey:          "tmdb-key",
		TVDBAPIKey:          "tvdb-key",
		AddonBaseURL:        "https://seedstream.example",
		LogLevel:            "debug",
		MemoryLimitMB:       2048,
		MovieSearchQueries:  []config.SearchQueryConfig{{Name: "{title} {year}"}},
		SeriesSearchQueries: []config.SearchQueryConfig{{Name: "{title} S{season:2}E{episode:2}"}},
		LoadedPath:          "/data/config.json",
	}

	next, err := mergeConfigPatch(current, []byte(`{"log_level":"info"}`))
	if err != nil {
		t.Fatalf("mergeConfigPatch: %v", err)
	}
	if next.LogLevel != "info" {
		t.Errorf("the edited field was not applied: log_level = %q", next.LogLevel)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"tmdb_api_key", next.TMDBAPIKey, "tmdb-key"},
		{"tvdb_api_key", next.TVDBAPIKey, "tvdb-key"},
		{"addon_base_url", next.AddonBaseURL, "https://seedstream.example"},
		{"loaded_path", next.LoadedPath, "/data/config.json"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s was destroyed by a partial save: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if next.MemoryLimitMB != 2048 {
		t.Errorf("memory_limit_mb was destroyed by a partial save: got %d", next.MemoryLimitMB)
	}
	if len(next.MovieSearchQueries) != 1 || len(next.SeriesSearchQueries) != 1 {
		t.Errorf("search queries were destroyed by a partial save: movie=%v series=%v",
			next.MovieSearchQueries, next.SeriesSearchQueries)
	}
}

// An explicit null or empty value IS an edit, and must still be applied —
// otherwise a field could never be cleared.
func TestConfigPatchStillAppliesExplicitClears(t *testing.T) {
	current := &config.Config{LogLevel: "debug", AddonBaseURL: "https://old.example"}
	next, err := mergeConfigPatch(current, []byte(`{"addon_base_url":""}`))
	if err != nil {
		t.Fatalf("mergeConfigPatch: %v", err)
	}
	if next.AddonBaseURL != "" {
		t.Errorf("an explicit clear was ignored: addon_base_url = %q", next.AddonBaseURL)
	}
	if next.LogLevel != "debug" {
		t.Errorf("an untouched field changed: log_level = %q", next.LogLevel)
	}
}
