package persistence

import (
	"database/sql"
	"time"
)

// IndexerStatsRow is the per-indexer aggregation over a [from,to) window,
// computed entirely from individual recorded events (NZBHydra2-style).
type IndexerStatsRow struct {
	IndexerName        string  `json:"indexer_name"`
	Searches           int64   `json:"searches"`
	SearchSharePct     float64 `json:"search_share_pct"`
	AvgResponseMS      float64 `json:"avg_response_ms"`
	AvgResults         float64 `json:"avg_results"`
	SuccessRatePct     float64 `json:"success_rate_pct"`
	Downloads          int64   `json:"downloads"`
	DownloadSharePct   float64 `json:"download_share_pct"`
	UniqueDownloads    int64   `json:"unique_downloads"`
	AvgUniquenessScore float64 `json:"avg_uniqueness_score"`
}

// ProviderStatsRow is the per-provider aggregation over a [from,to) window,
// computed from recorded provider access deltas. Only measured values are
// included (there is no per-provider response time measurement, so it is
// intentionally omitted).
type ProviderStatsRow struct {
	ProviderName    string  `json:"provider_name"`
	Host            string  `json:"host"`
	Articles        int64   `json:"articles"`
	Available       int64   `json:"available"`
	Missing         int64   `json:"missing"`
	SuccessRatePct  float64 `json:"success_rate_pct"`
	DownloadedBytes int64   `json:"downloaded_bytes"`
	DataSharePct    float64 `json:"data_share_pct"`
}

// RecordIndexerSearchEvent inserts a single search event.
func (m *StateManager) RecordIndexerSearchEvent(ts time.Time, indexerName string, success bool, responseMS int64, resultCount int) error {
	if ts.IsZero() {
		ts = time.Now()
	}
	return m.withWriteLock(func(db *sql.DB) error {
		_, err := db.Exec(
			`INSERT INTO indexer_search_events (ts, indexer_name, success, response_ms, result_count) VALUES (?, ?, ?, ?, ?)`,
			ts.Unix(), indexerName, boolToInt(success), responseMS, resultCount,
		)
		return err
	})
}

// RecordIndexerDownloadEvent inserts a single download event.
func (m *StateManager) RecordIndexerDownloadEvent(ts time.Time, indexerName string, success bool, indexersInSearch, indexersWithResult int) error {
	if ts.IsZero() {
		ts = time.Now()
	}
	if indexersInSearch < 1 {
		indexersInSearch = 1
	}
	if indexersWithResult < 1 {
		indexersWithResult = 1
	}
	return m.withWriteLock(func(db *sql.DB) error {
		_, err := db.Exec(
			`INSERT INTO indexer_download_events (ts, indexer_name, success, indexers_in_search, indexers_with_result) VALUES (?, ?, ?, ?, ?)`,
			ts.Unix(), indexerName, boolToInt(success), indexersInSearch, indexersWithResult,
		)
		return err
	})
}

// RecordProviderAccessDelta inserts a single provider delta row.
func (m *StateManager) RecordProviderAccessDelta(ts time.Time, providerName, host string, availableDelta, missingDelta, bytesDelta int64) error {
	if ts.IsZero() {
		ts = time.Now()
	}
	return m.withWriteLock(func(db *sql.DB) error {
		_, err := db.Exec(
			`INSERT INTO provider_access_deltas (ts, provider_name, host, available_delta, missing_delta, bytes_delta) VALUES (?, ?, ?, ?, ?, ?)`,
			ts.Unix(), providerName, host, availableDelta, missingDelta, bytesDelta,
		)
		return err
	})
}

// eventRange returns the unix-second bounds for a [from,to) window. Nil bounds
// become the widest possible range so callers can pass open ranges.
func eventRange(from, to *time.Time) (int64, int64) {
	var lo int64 = 0
	var hi int64 = 1<<62 - 1
	if from != nil {
		lo = from.Unix()
	}
	if to != nil {
		hi = to.Unix()
	}
	return lo, hi
}

// GetIndexerStats aggregates per-indexer search and download events over the
// [from,to) window. Shares are computed against the window totals.
//
// Aggregation SQL (per indexer over the window):
//
//	searches        = COUNT(*) of search events
//	avg_response_ms = AVG(response_ms) WHERE success = 1
//	avg_results     = AVG(result_count) WHERE success = 1
//	success_rate    = 100 * SUM(success) / COUNT(*)
//	downloads       = COUNT(*) of successful download events
//	unique_downloads= COUNT(*) of download events WHERE indexers_with_result = 1
//	uniqueness      = AVG(100.0 * indexers_in_search / indexers_with_result) over download events
//
// search_share_pct and download_share_pct are then computed in Go as a
// percentage of the window totals across all indexers.
func (m *StateManager) GetIndexerStats(from, to *time.Time) ([]IndexerStatsRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lo, hi := eventRange(from, to)

	rows := make(map[string]*IndexerStatsRow)
	get := func(name string) *IndexerStatsRow {
		r, ok := rows[name]
		if !ok {
			r = &IndexerStatsRow{IndexerName: name}
			rows[name] = r
		}
		return r
	}

	// Search aggregation. AVG over successful events only; success_rate over all.
	searchRows, err := m.db.Query(`
		SELECT
			indexer_name,
			COUNT(*) AS searches,
			COALESCE(AVG(CASE WHEN success = 1 THEN response_ms END), 0) AS avg_response_ms,
			COALESCE(AVG(CASE WHEN success = 1 THEN result_count END), 0) AS avg_results,
			SUM(success) AS successes
		FROM indexer_search_events
		WHERE ts >= ? AND ts < ?
		GROUP BY indexer_name
	`, lo, hi)
	if err != nil {
		return nil, err
	}
	var totalSearches int64
	for searchRows.Next() {
		var (
			name      string
			searches  int64
			avgResp   float64
			avgRes    float64
			successes int64
		)
		if err := searchRows.Scan(&name, &searches, &avgResp, &avgRes, &successes); err != nil {
			searchRows.Close()
			return nil, err
		}
		r := get(name)
		r.Searches = searches
		r.AvgResponseMS = avgResp
		r.AvgResults = avgRes
		if searches > 0 {
			r.SuccessRatePct = 100.0 * float64(successes) / float64(searches)
		}
		totalSearches += searches
	}
	if err := searchRows.Err(); err != nil {
		searchRows.Close()
		return nil, err
	}
	searchRows.Close()

	// Download aggregation. downloads = successful downloads only.
	dlRows, err := m.db.Query(`
		SELECT
			indexer_name,
			SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) AS downloads,
			SUM(CASE WHEN indexers_with_result = 1 THEN 1 ELSE 0 END) AS unique_downloads,
			COALESCE(AVG(100.0 * indexers_in_search / indexers_with_result), 0) AS avg_uniqueness
		FROM indexer_download_events
		WHERE ts >= ? AND ts < ?
		GROUP BY indexer_name
	`, lo, hi)
	if err != nil {
		return nil, err
	}
	var totalDownloads int64
	for dlRows.Next() {
		var (
			name          string
			downloads     int64
			uniqueDl      int64
			avgUniqueness float64
		)
		if err := dlRows.Scan(&name, &downloads, &uniqueDl, &avgUniqueness); err != nil {
			dlRows.Close()
			return nil, err
		}
		r := get(name)
		r.Downloads = downloads
		r.UniqueDownloads = uniqueDl
		r.AvgUniquenessScore = avgUniqueness
		totalDownloads += downloads
	}
	if err := dlRows.Err(); err != nil {
		dlRows.Close()
		return nil, err
	}
	dlRows.Close()

	out := make([]IndexerStatsRow, 0, len(rows))
	for _, r := range rows {
		if totalSearches > 0 {
			r.SearchSharePct = 100.0 * float64(r.Searches) / float64(totalSearches)
		}
		if totalDownloads > 0 {
			r.DownloadSharePct = 100.0 * float64(r.Downloads) / float64(totalDownloads)
		}
		out = append(out, *r)
	}
	return out, nil
}

// GetProviderStats aggregates per-provider access deltas over the [from,to)
// window. Only measured values are returned. data_share_pct is the share of
// downloaded bytes across all providers in the window.
func (m *StateManager) GetProviderStats(from, to *time.Time) ([]ProviderStatsRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	lo, hi := eventRange(from, to)

	rows, err := m.db.Query(`
		SELECT
			provider_name,
			COALESCE(MAX(host), '') AS host,
			SUM(available_delta) AS available,
			SUM(missing_delta) AS missing,
			SUM(bytes_delta) AS bytes
		FROM provider_access_deltas
		WHERE ts >= ? AND ts < ?
		GROUP BY provider_name
	`, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProviderStatsRow, 0)
	var totalBytes int64
	for rows.Next() {
		var r ProviderStatsRow
		if err := rows.Scan(&r.ProviderName, &r.Host, &r.Available, &r.Missing, &r.DownloadedBytes); err != nil {
			return nil, err
		}
		r.Articles = r.Available + r.Missing
		if r.Articles > 0 {
			r.SuccessRatePct = 100.0 * float64(r.Available) / float64(r.Articles)
		}
		totalBytes += r.DownloadedBytes
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if totalBytes > 0 {
		for i := range out {
			out[i].DataSharePct = 100.0 * float64(out[i].DownloadedBytes) / float64(totalBytes)
		}
	}
	return out, nil
}
