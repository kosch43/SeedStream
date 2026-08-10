package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveKeepsPreviousConfigAsBackup: the temp-and-rename write is atomic, so
// the file on disk is never half-written — but atomicity says nothing about
// the content being right. A save that is well-formed and simply wrong
// overwrites the good config just as completely, and in the field there was
// nothing to go back to.
func TestSaveKeepsPreviousConfigAsBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	first := &Config{AddonBaseURL: "https://first.example", LogLevel: "debug"}
	if err := first.SaveFile(path); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := os.Stat(path + configBackupSuffix); !os.IsNotExist(err) {
		t.Errorf("the very first save has nothing to back up, but wrote %s", path+configBackupSuffix)
	}

	second := &Config{AddonBaseURL: "https://second.example", LogLevel: "info"}
	if err := second.SaveFile(path); err != nil {
		t.Fatalf("second save: %v", err)
	}

	var backup Config
	data, err := os.ReadFile(path + configBackupSuffix)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if err := json.Unmarshal(data, &backup); err != nil {
		t.Fatalf("backup is not valid JSON: %v", err)
	}
	if backup.AddonBaseURL != "https://first.example" {
		t.Errorf("backup holds %q, want the config as it was before the last save", backup.AddonBaseURL)
	}

	var current Config
	if err := current.LoadFile(path); err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if current.AddonBaseURL != "https://second.example" {
		t.Errorf("live config holds %q, want the newly saved value", current.AddonBaseURL)
	}
}

// TestSaveNeverBlanksAStreamToken: a stream's token is the whole of its
// authentication, and AuthenticateToken can never match an empty one — so a
// save that drops it locks the viewer out of every request while the app still
// reports healthy. Absent means unchanged, exactly as for the admin secrets.
func TestSaveNeverBlanksAStreamToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := &Config{Streams: map[string]*StreamEntry{
		"default": {Username: "default", Token: "keep-this-token"},
		"alice":   {Username: "alice", Token: "alice-token"},
	}}
	if err := original.SaveFile(path); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// A payload that carries the streams but not their tokens.
	partial := &Config{Streams: map[string]*StreamEntry{
		"default": {Username: "default"},
		"alice":   {Username: "alice", Token: "alice-rotated"},
	}}
	if err := partial.SaveFile(path); err != nil {
		t.Fatalf("second save: %v", err)
	}

	var saved Config
	if err := saved.LoadFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := saved.Streams["default"].Token; got != "keep-this-token" {
		t.Errorf("default stream token = %q, want the previously saved token", got)
	}
	if got := saved.Streams["alice"].Token; got != "alice-rotated" {
		t.Errorf("a deliberate token change was reverted: %q", got)
	}
}
