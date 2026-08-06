package cardigann

import (
	"strings"
	"testing"
)

// TestBundledDefinitionsLoad pins the tracker list users actually get. The
// picker is only useful if it offers real trackers, and a schema regression
// that silently rejected most definitions would leave it near-empty while
// everything still compiled.
func TestBundledDefinitionsLoad(t *testing.T) {
	c := NewCatalog(t.TempDir())
	loaded, failed := c.Reload()

	if loaded < 500 {
		t.Fatalf("only %d definitions loaded (%d rejected) — expected the full bundled set", loaded, failed)
	}
	t.Logf("loaded=%d rejected=%d", loaded, failed)
}

// TestBundledDefinitionsCarryURLs is the point of bundling them: a user picks a
// tracker and its address is already filled in.
func TestBundledDefinitionsCarryURLs(t *testing.T) {
	c := NewCatalog(t.TempDir())
	withURL := 0
	for _, e := range c.Search("", 5000) {
		if len(e.Links) > 0 && strings.HasPrefix(e.Links[0], "http") {
			withURL++
		}
	}
	if withURL < 500 {
		t.Fatalf("only %d entries carry a URL — the picker would not be preconfigured", withURL)
	}
}

// TestSearchFindsWellKnownTrackers spot-checks a few by name.
func TestSearchFindsWellKnownTrackers(t *testing.T) {
	c := NewCatalog(t.TempDir())
	for _, name := range []string{"torrentleech", "1337x", "nyaa"} {
		if got := c.Search(name, 10); len(got) == 0 {
			t.Errorf("searching %q returned nothing", name)
		}
	}
}
