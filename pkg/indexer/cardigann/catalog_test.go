package cardigann

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalDef = `
id: %s
name: %s
type: private
links:
  - https://%s.invalid/
settings:
  - name: username
    type: text
    label: Username
  - name: passkey
    type: password
    label: Passkey
  - name: note
    type: info
    label: Read me
search:
  paths:
    - path: browse.php
  rows:
    selector: tr.torrent
  fields:
    title:
      selector: a.name
    download:
      selector: a.dl
      attribute: href
`

func writeDef(t *testing.T, dir, id, name string) {
	t.Helper()
	content := strings.Replace(minimalDef, "%s", id, 1)
	content = strings.Replace(content, "%s", name, 1)
	content = strings.Replace(content, "%s", id, 1)
	if err := os.WriteFile(filepath.Join(dir, id+".yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write definition: %v", err)
	}
}

// TestCatalogLoadsAndSearches covers the tracker picker's data source: files on
// disk become a searchable list without a rebuild.
func TestCatalogLoadsAndSearches(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "alphatracker", "Alpha Tracker")
	writeDef(t, dir, "betatracker", "Beta Tracker")

	c := NewCatalog(dir)
	if c.Count() < 2 {
		t.Fatalf("expected at least the two written definitions, got %d", c.Count())
	}
	if _, ok := c.Get("alphatracker"); !ok {
		t.Fatal("definition should be retrievable by id")
	}
	// Ids are matched case-insensitively so the UI need not care.
	if _, ok := c.Get("AlphaTracker"); !ok {
		t.Fatal("id lookup should be case-insensitive")
	}

	hits := c.Search("beta", 0)
	if len(hits) != 1 || hits[0].ID != "betatracker" {
		t.Fatalf("search did not find the expected tracker: %+v", hits)
	}

	// Only real credentials are offered to the user; info blocks are not fields.
	for _, s := range hits[0].Settings {
		if s.Type == "info" {
			t.Fatal("informational settings must not be presented as credential fields")
		}
	}
	if len(hits[0].Settings) != 2 {
		t.Fatalf("expected username and passkey, got %d settings", len(hits[0].Settings))
	}
}

// TestUserDefinitionOverridesBundled: a definition dropped on disk replaces a
// bundled one with the same id, which is how a stale shipped definition gets
// corrected without waiting for a release.
func TestUserDefinitionOverridesBundled(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "example-tracker", "Corrected Example")

	c := NewCatalog(dir)
	def, ok := c.Get("example-tracker")
	if !ok {
		t.Fatal("definition missing")
	}
	if def.Name != "Corrected Example" {
		t.Fatalf("disk definition should win over the bundled one, got %q", def.Name)
	}
}

// TestInstallDefinitionRejectsInvalid: a broken file must never be written where
// it would be loaded at startup.
func TestInstallDefinitionRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	if _, err := c.InstallDefinition("bad.yml", []byte("this: [is not: a definition")); err == nil {
		t.Fatal("malformed YAML must be rejected")
	}
	if _, err := c.InstallDefinition("nolinks.yml", []byte("id: x\nname: X\n")); err == nil {
		t.Fatal("a definition with no links must be rejected")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("nothing should have been written, found %d files", len(entries))
	}
}

// TestInstallDefinitionStoresValid and makes it immediately usable.
func TestInstallDefinitionStoresValid(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog(dir)

	content := strings.Replace(minimalDef, "%s", "newtracker", 1)
	content = strings.Replace(content, "%s", "New Tracker", 1)
	content = strings.Replace(content, "%s", "newtracker", 1)

	def, err := c.InstallDefinition("newtracker", []byte(content))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if def.ID != "newtracker" {
		t.Fatalf("unexpected id %q", def.ID)
	}
	if _, ok := c.Get("newtracker"); !ok {
		t.Fatal("installed definition should be available immediately")
	}
}

// TestBaseURLPrefersOverride pins the behaviour that keeps a tracker reachable
// after it changes domain.
func TestBaseURLPrefersOverride(t *testing.T) {
	content := strings.Replace(minimalDef, "%s", "movedtracker", 1)
	content = strings.Replace(content, "%s", "Moved Tracker", 1)
	content = strings.Replace(content, "%s", "movedtracker", 1)

	def, err := Parse([]byte(content))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := def.BaseURL(""); got != "https://movedtracker.invalid/" {
		t.Fatalf("default base URL wrong: %q", got)
	}
	if got := def.BaseURL("https://new-home.example"); got != "https://new-home.example/" {
		t.Fatalf("override should win: %q", got)
	}
}
