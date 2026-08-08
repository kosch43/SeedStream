package cardigann

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"seedstream/pkg/core/logger"
)

//go:embed definitions/*.yml
var bundled embed.FS

// Catalog holds every tracker definition SeedStream knows about, so the UI can
// present a searchable list and the engine can build a client from a chosen id.
//
// Definitions come from two places: a small bundled set, and a directory on disk
// that the operator can fill with the published Jackett/Prowlarr definition
// files. Disk wins on an id clash, so a bundled definition can be corrected
// without a rebuild.
type Catalog struct {
	mu   sync.RWMutex
	defs map[string]*Definition
	dir  string
}

// CatalogEntry is the summary the UI lists.
type CatalogEntry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Language    string    `json:"language"`
	Type        string    `json:"type"`
	Links       []string  `json:"links"`
	Settings    []Setting `json:"settings"`
	NeedsCookie bool      `json:"needs_cookie"`
	Source      string    `json:"source"` // "bundled" or "user"
}

// NewCatalog loads the bundled definitions plus anything in dir.
func NewCatalog(dir string) *Catalog {
	c := &Catalog{defs: map[string]*Definition{}, dir: dir}
	c.Reload()
	return c
}

// DefinitionsDir returns where user-supplied definitions are read from.
func (c *Catalog) DefinitionsDir() string { return c.dir }

// Reload re-reads every definition from disk. Called at startup and whenever the
// operator imports a new definition set, so new trackers appear without a
// restart.
func (c *Catalog) Reload() (loaded int, failed int) {
	defs := map[string]*Definition{}

	// Bundled definitions first so disk can override them.
	if entries, err := bundled.ReadDir("definitions"); err == nil {
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			data, err := bundled.ReadFile(filepath.Join("definitions", ent.Name()))
			if err != nil {
				failed++
				continue
			}
			d, err := Parse(data)
			if err != nil {
				logger.Debug("cardigann: bundled definition rejected", "file", ent.Name(), "err", err)
				failed++
				continue
			}
			d.sourceIsUser = false
			defs[strings.ToLower(d.ID)] = d
			loaded++
		}
	}

	if c.dir != "" {
		_ = filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".yml" && ext != ".yaml" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				failed++
				return nil
			}
			def, err := Parse(data)
			if err != nil {
				logger.Debug("cardigann: definition rejected", "file", path, "err", err)
				failed++
				return nil
			}
			def.sourceIsUser = true
			if _, replacing := defs[strings.ToLower(def.ID)]; !replacing {
				loaded++
			}
			defs[strings.ToLower(def.ID)] = def
			return nil
		})
	}

	c.mu.Lock()
	c.defs = defs
	c.mu.Unlock()
	logger.Info("cardigann: tracker definitions loaded", "count", len(defs), "rejected", failed, "dir", c.dir)
	return len(defs), failed
}

// Get returns a definition by id.
func (c *Catalog) Get(id string) (*Definition, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.defs[strings.ToLower(strings.TrimSpace(id))]
	return d, ok
}

// Count is how many definitions are loaded.
func (c *Catalog) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.defs)
}

// Search lists definitions whose name, id or description matches the query,
// sorted by name. An empty query lists everything, which is what the tracker
// picker shows before the user types.
func (c *Catalog) Search(query string, limit int) []CatalogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]CatalogEntry, 0, len(c.defs))
	for _, d := range c.defs {
		if q != "" && !matches(d, q) {
			continue
		}
		src := "bundled"
		if d.sourceIsUser {
			src = "user"
		}
		out = append(out, CatalogEntry{
			ID:          d.ID,
			Name:        d.Name,
			Description: d.Description,
			Language:    d.Language,
			Type:        d.Type,
			Links:       d.LinkURLs(),
			Settings:    d.Credentials(),
			NeedsCookie: d.NeedsCaptcha(),
			Source:      src,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func matches(d *Definition, q string) bool {
	return strings.Contains(strings.ToLower(d.Name), q) ||
		strings.Contains(strings.ToLower(d.ID), q) ||
		strings.Contains(strings.ToLower(d.Description), q)
}

// InstallDefinition writes a definition file into the user directory after
// validating it, so a bad file can never be stored where it would break startup.
func (c *Catalog) InstallDefinition(filename string, data []byte) (*Definition, error) {
	if c.dir == "" {
		return nil, fmt.Errorf("no definitions directory configured")
	}
	def, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(filepath.Base(filename))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = def.ID + ".yml"
	}
	if ext := strings.ToLower(filepath.Ext(name)); ext != ".yml" && ext != ".yaml" {
		name += ".yml"
	}
	if err := os.WriteFile(filepath.Join(c.dir, name), data, 0o644); err != nil {
		return nil, err
	}
	c.Reload()
	return def, nil
}
