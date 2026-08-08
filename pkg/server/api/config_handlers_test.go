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
