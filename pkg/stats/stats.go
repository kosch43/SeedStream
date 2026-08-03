// Package stats records individual tracker statistics events (searches,
// downloads, provider access deltas) for query-time aggregation, mirroring
// NZBHydra2's event-based model. Recording is non-blocking on the request path
// and never panics or surfaces DB errors to callers — failures are logged at
// debug level and dropped.
package stats

import (
	"sync"
	"time"

	"seedstream/pkg/core/logger"
)

// Recorder records measured statistics events. Implementations must be safe
// for concurrent use and must not block or panic on the request path.
type Recorder interface {
	// RecordIndexerSearch records a single search against one indexer.
	RecordIndexerSearch(name string, success bool, responseMS int64, resultCount int)
	// RecordIndexerDownload records a single NZB download from one indexer.
	// indexersInSearch is how many indexers participated in the originating
	// search; indexersWithResult is how many returned this same result.
	RecordIndexerDownload(name string, success bool, indexersInSearch, indexersWithResult int)
	// RecordProviderDelta records the increment (since the last tick) in a
	// provider's available/missing article counts and downloaded bytes.
	RecordProviderDelta(name, host string, availableDelta, missingDelta, bytesDelta int64)
}

// EventStore is the persistence surface the SQLite recorder depends on. It is
// satisfied by *persistence.StateManager.
type EventStore interface {
	RecordIndexerSearchEvent(ts time.Time, indexerName string, success bool, responseMS int64, resultCount int) error
	RecordIndexerDownloadEvent(ts time.Time, indexerName string, success bool, indexersInSearch, indexersWithResult int) error
	RecordProviderAccessDelta(ts time.Time, providerName, host string, availableDelta, missingDelta, bytesDelta int64) error
}

// NopRecorder discards all events. It is the default until SetDefault is called.
type NopRecorder struct{}

func (NopRecorder) RecordIndexerSearch(string, bool, int64, int)            {}
func (NopRecorder) RecordIndexerDownload(string, bool, int, int)            {}
func (NopRecorder) RecordProviderDelta(string, string, int64, int64, int64) {}

// sqliteRecorder writes events through an EventStore. All errors are logged at
// debug and swallowed so the request path is never affected.
type sqliteRecorder struct {
	store EventStore
}

// NewSQLiteRecorder wraps an EventStore (e.g. *persistence.StateManager).
func NewSQLiteRecorder(store EventStore) Recorder {
	if store == nil {
		return NopRecorder{}
	}
	return &sqliteRecorder{store: store}
}

func (r *sqliteRecorder) RecordIndexerSearch(name string, success bool, responseMS int64, resultCount int) {
	if r == nil || r.store == nil {
		return
	}
	if err := r.store.RecordIndexerSearchEvent(time.Now(), name, success, responseMS, resultCount); err != nil {
		logger.Debug("stats: record indexer search failed", "indexer", name, "err", err)
	}
}

func (r *sqliteRecorder) RecordIndexerDownload(name string, success bool, indexersInSearch, indexersWithResult int) {
	if r == nil || r.store == nil {
		return
	}
	if err := r.store.RecordIndexerDownloadEvent(time.Now(), name, success, indexersInSearch, indexersWithResult); err != nil {
		logger.Debug("stats: record indexer download failed", "indexer", name, "err", err)
	}
}

func (r *sqliteRecorder) RecordProviderDelta(name, host string, availableDelta, missingDelta, bytesDelta int64) {
	if r == nil || r.store == nil {
		return
	}
	if err := r.store.RecordProviderAccessDelta(time.Now(), name, host, availableDelta, missingDelta, bytesDelta); err != nil {
		logger.Debug("stats: record provider delta failed", "provider", name, "err", err)
	}
}

var (
	defaultMu       sync.RWMutex
	defaultRecorder Recorder = NopRecorder{}
)

// Default returns the package-global recorder. It is never nil.
func Default() Recorder {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultRecorder
}

// SetDefault installs the package-global recorder (call once at startup). A nil
// argument resets to the no-op recorder.
func SetDefault(r Recorder) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if r == nil {
		defaultRecorder = NopRecorder{}
		return
	}
	defaultRecorder = r
}
