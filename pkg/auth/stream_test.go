package auth

import (
	"testing"

	"seedstream/pkg/core/config"
)

func TestSetConfigReloadsInMemoryStreamsFromConfig(t *testing.T) {
	cfg := &config.Config{
		Streams: map[string]*config.StreamEntry{
			"default": {
				Username:           "default",
				Token:              "token-default",
				AutoAddIndexers:    ptrBool(true),
				IndexerSelections:  []string{"IndexerA"},
			},
		},
	}

	dm := &StreamManager{
		streams: map[string]*Stream{
			"default": {
				Username:           "default",
				Token:              "old-token",
				AutoAddIndexers:    ptrBool(true),
				IndexerSelections:  []string{"IndexerA", "IndexerB"},
			},
		},
	}

	dm.SetConfig(cfg, nil)

	stream, err := dm.GetStream("default", "admin")
	if err != nil {
		t.Fatalf("GetStream returned error: %v", err)
	}
	if stream.Token != "token-default" {
		t.Fatalf("expected token to come from config, got %q", stream.Token)
	}
	if len(stream.IndexerSelections) != 1 || stream.IndexerSelections[0] != "IndexerA" {
		t.Fatalf("expected indexer selections to reload from config, got %#v", stream.IndexerSelections)
	}
}
