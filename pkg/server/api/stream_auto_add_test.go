package api

import (
	"reflect"
	"testing"

	"seedstream/pkg/core/config"
)

func boolPtr(v bool) *bool { return &v }

func TestApplyStreamAutoSelectionsSyncsEnabledAndKeepsOrderPreference(t *testing.T) {
	nextCfg := &config.Config{
		Indexers: []config.IndexerConfig{
			{Name: "TrackerA", Enabled: boolPtr(true)},
			{Name: "TrackerB", Enabled: boolPtr(false)},
			{Name: "TrackerC", Enabled: boolPtr(true)},
		},
		Streams: map[string]*config.StreamEntry{
			"default": {
				AutoAddIndexers:   boolPtr(true),
				IndexerSelections: []string{"TrackerC", "TrackerB", "TrackerA", "Missing"},
				IndexerOverrides: map[string]config.IndexerSearchConfig{
					"TrackerC": {SearchResultLimit: 10},
					"TrackerB": {SearchResultLimit: 20},
				},
			},
			"custom": {
				AutoAddIndexers:   boolPtr(false),
				IndexerSelections: []string{"TrackerA", "TrackerB"},
			},
		},
	}

	applyStreamAutoSelections(nextCfg)

	if got := nextCfg.Streams["default"].IndexerSelections; !reflect.DeepEqual(got, []string{"TrackerC", "TrackerA"}) {
		t.Fatalf("default tracker selections = %#v", got)
	}
	if _, ok := nextCfg.Streams["default"].IndexerOverrides["TrackerB"]; ok {
		t.Fatalf("expected disabled tracker override to be removed")
	}
	if _, ok := nextCfg.Streams["default"].IndexerOverrides["TrackerC"]; !ok {
		t.Fatalf("expected enabled tracker override to remain")
	}
	if got := nextCfg.Streams["custom"].IndexerSelections; !reflect.DeepEqual(got, []string{"TrackerA", "TrackerB"}) {
		t.Fatalf("custom tracker selections = %#v", got)
	}
}

func TestApplyStreamAutoSelectionsSkipsWhenFlagMissing(t *testing.T) {
	nextCfg := &config.Config{
		Indexers: []config.IndexerConfig{
			{Name: "TrackerA", Enabled: boolPtr(true)},
			{Name: "TrackerB", Enabled: boolPtr(false)},
		},
		Streams: map[string]*config.StreamEntry{
			"stream": {
				IndexerSelections: []string{"TrackerA"},
			},
		},
	}

	applyStreamAutoSelections(nextCfg)

	if got := nextCfg.Streams["stream"].IndexerSelections; !reflect.DeepEqual(got, []string{"TrackerA"}) {
		t.Fatalf("tracker selections = %#v", got)
	}
}

func TestApplyStreamNameRenamesRenamesSelectionsAndOverrides(t *testing.T) {
	streams := map[string]*config.StreamEntry{
		"stream": {
			IndexerSelections: []string{"TrackerOne", "TrackerTwo"},
			IndexerOverrides: map[string]config.IndexerSearchConfig{
				"TrackerOne": {SearchResultLimit: 10},
				"TrackerTwo": {SearchResultLimit: 20},
			},
		},
	}

	applyStreamNameRenames(streams, map[string]string{"trackerone": "TrackerRenamed"})

	if got := streams["stream"].IndexerSelections; !reflect.DeepEqual(got, []string{"TrackerRenamed", "TrackerTwo"}) {
		t.Fatalf("tracker selections = %#v", got)
	}
	if _, ok := streams["stream"].IndexerOverrides["TrackerRenamed"]; !ok {
		t.Fatalf("expected renamed override to exist")
	}
	if _, ok := streams["stream"].IndexerOverrides["TrackerOne"]; ok {
		t.Fatalf("expected old override key to be removed")
	}
}

func TestRenamedNamesByIndexDetectsNameChanges(t *testing.T) {
	current := []config.IndexerConfig{{Name: "TrackerOne"}, {Name: "TrackerTwo"}}
	next := []config.IndexerConfig{{Name: "TrackerRenamed"}, {Name: "TrackerTwo"}}

	renames := renamedNamesByIndex(current, next, func(idx config.IndexerConfig) string { return idx.Name })

	if got := renames["trackerone"]; got != "TrackerRenamed" {
		t.Fatalf("tracker rename map = %#v", renames)
	}
	if _, exists := renames["trackertwo"]; exists {
		t.Fatalf("unexpected rename for unchanged tracker: %#v", renames)
	}
}
