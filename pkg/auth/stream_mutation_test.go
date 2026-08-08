package auth

import (
	"errors"
	"reflect"
	"testing"

	"seedstream/pkg/core/config"
)

func mutationTestManager(saveFn func() error) *StreamManager {
	client := &config.TorrentClientConfig{
		Name:     "box",
		URL:      "https://box.example",
		Username: "torrent-user",
		Password: "torrent-password",
	}
	stream := &Stream{
		Username:          "alice",
		Token:             "old-token",
		Order:             3,
		FilterSortingMode: "none",
		IndexerMode:       "combine",
		CombineResults:    ptrBool(true),
		EnableFailover:    ptrBool(true),
		ResultsMode:       "combined_stream",
		AutoAddIndexers:   ptrBool(false),
		IndexerOverrides: map[string]config.IndexerSearchConfig{
			"old": {SearchResultLimit: 1},
		},
		IndexerSelections:   []string{"old"},
		MovieSearchQueries:  []string{"old-movie"},
		SeriesSearchQueries: []string{"old-series"},
		TorrentClient:       client,
		ProwlarrURL:         "https://prowlarr.example",
		ProwlarrAPIKey:      "prowlarr-secret",
		PasswordHash:        "password-hash",
		MustChangePassword:  true,
	}
	return &StreamManager{
		streams: map[string]*Stream{"alice": stream},
		cfg: &config.Config{Streams: map[string]*config.StreamEntry{
			"alice": {Username: "alice", Token: "old-token"},
		}},
		saveFn: saveFn,
	}
}

func TestUpdateStreamAssignmentsPreservesTokenAndHiddenFields(t *testing.T) {
	dm := mutationTestManager(func() error { return nil })
	original := dm.streams["alice"]
	client := original.TorrentClient

	if err := dm.UpdateStreamConfig("alice", &Stream{
		FilterSortingMode:   "aiostreams",
		IndexerMode:         "failover",
		CombineResults:      ptrBool(false),
		EnableFailover:      ptrBool(false),
		ResultsMode:         "display_all",
		AutoAddIndexers:     ptrBool(true),
		IndexerOverrides:    map[string]config.IndexerSearchConfig{"new": {SearchResultLimit: 5}},
		IndexerSelections:   []string{"new"},
		MovieSearchQueries:  []string{"new-movie"},
		SeriesSearchQueries: []string{"new-series"},
		// These fields must not be accepted from an assignment update.
		Token:          "attacker-token",
		TorrentClient:  &config.TorrentClientConfig{Password: "attacker-password"},
		ProwlarrAPIKey: "attacker-key",
		PasswordHash:   "attacker-hash",
	}); err != nil {
		t.Fatalf("UpdateStreamConfig returned error: %v", err)
	}

	got := dm.streams["alice"]
	if got.Token != "old-token" || got.ProwlarrAPIKey != "prowlarr-secret" || got.PasswordHash != "password-hash" || !got.MustChangePassword {
		t.Fatalf("assignment update changed hidden fields: %#v", got)
	}
	if got.TorrentClient != client || got.TorrentClient.Password != "torrent-password" {
		t.Fatalf("assignment update changed torrent credentials: %#v", got.TorrentClient)
	}
	if !reflect.DeepEqual(got.IndexerSelections, []string{"new"}) || !reflect.DeepEqual(got.MovieSearchQueries, []string{"new-movie"}) {
		t.Fatalf("assignment fields were not updated: %#v", got)
	}
}

func TestCreateStreamReturnsCanonicalUsernameAndToken(t *testing.T) {
	dm := &StreamManager{
		streams: make(map[string]*Stream),
		cfg:     &config.Config{Streams: make(map[string]*config.StreamEntry)},
		saveFn:  func() error { return nil },
	}

	stream, err := dm.CreateStream("  Bob ", "", "admin")
	if err != nil {
		t.Fatalf("CreateStream returned error: %v", err)
	}
	if stream.Username != "bob" {
		t.Fatalf("canonical username = %q, want bob", stream.Username)
	}
	if stream.Token == "" {
		t.Fatal("CreateStream must return the generated token")
	}
	if _, exists := dm.streams[stream.Username]; !exists {
		t.Fatalf("canonical stream key %q was not stored", stream.Username)
	}
}

func TestCreateStreamRollsBackWhenSaveFails(t *testing.T) {
	dm := &StreamManager{
		streams: map[string]*Stream{},
		cfg:     &config.Config{Streams: make(map[string]*config.StreamEntry)},
		saveFn:  func() error { return errors.New("save failed") },
	}
	if _, err := dm.CreateStream("bob", "", "admin"); err == nil {
		t.Fatal("CreateStream should return the save error")
	}
	if len(dm.streams) != 0 {
		t.Fatalf("create failure left in-memory streams: %#v", dm.streams)
	}
	if len(dm.cfg.Streams) != 0 {
		t.Fatalf("create failure left config streams: %#v", dm.cfg.Streams)
	}
}

func TestDeleteStreamRollsBackWhenSaveFails(t *testing.T) {
	dm := mutationTestManager(func() error { return errors.New("save failed") })
	if err := dm.DeleteStream("alice"); err == nil {
		t.Fatal("DeleteStream should return the save error")
	}
	if _, exists := dm.streams["alice"]; !exists {
		t.Fatal("delete failure must restore the in-memory stream")
	}
	if _, exists := dm.cfg.Streams["alice"]; !exists {
		t.Fatal("delete failure must restore config stream state")
	}
}

func TestRegenerateTokenRollsBackWhenSaveFails(t *testing.T) {
	dm := mutationTestManager(func() error { return errors.New("save failed") })
	if _, err := dm.RegenerateToken("alice"); err == nil {
		t.Fatal("RegenerateToken should return the save error")
	}
	if got := dm.streams["alice"].Token; got != "old-token" {
		t.Fatalf("token after failed regeneration = %q, want old-token", got)
	}
	if got := dm.cfg.Streams["alice"].Token; got != "old-token" {
		t.Fatalf("config token after failed regeneration = %q, want old-token", got)
	}
}
