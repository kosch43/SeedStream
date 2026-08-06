package initialization

import (
	"path/filepath"
	"sync"

	"seedstream/pkg/indexer/cardigann"
)

var (
	catalogOnce sync.Once
	catalog     *cardigann.Catalog
)

// TrackerCatalog returns the process-wide tracker definition catalog, loading it
// on first use from the bundled set plus the operator's definitions directory.
//
// It is a singleton because definitions are read from disk and shared by both
// the search stack (which builds tracker clients from them) and the API (which
// lists them in the tracker picker), and neither should reload the set
// independently.
func TrackerCatalog(dataDir string) *cardigann.Catalog {
	catalogOnce.Do(func() {
		catalog = cardigann.NewCatalog(filepath.Join(dataDir, "definitions"))
	})
	return catalog
}
