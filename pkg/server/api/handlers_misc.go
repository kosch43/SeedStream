package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/indexer"
)

type persistedStatsResponse struct {
	Indexers  []persistence.IndexerMetric  `json:"indexers"`
}

func parseDateParam(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// Parse date-only filters in local server time so the selected calendar day
	// aligns with user expectations instead of being interpreted as UTC.
	t, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Server) handleGetIndexerCaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	caps := s.indexerCaps
	s.mu.RUnlock()
	if caps == nil {
		caps = make(map[string]*indexer.Caps)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(caps)
}

func (s *Server) handleRefreshIndexerCaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	idx := s.indexer
	s.mu.RUnlock()
	caps := make(map[string]*indexer.Caps)
	if agg, ok := idx.(*indexer.Aggregator); ok {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, i := range agg.Indexers {
			if c, ok := i.(indexer.IndexerWithCaps); ok {
				wg.Add(1)
				go func(name string, fetcher indexer.IndexerWithCaps) {
					defer wg.Done()
					if result, err := fetcher.GetCaps(); err == nil {
						mu.Lock()
						caps[name] = result
						mu.Unlock()
					}
				}(i.Name(), c)
			}
		}
		wg.Wait()
	}
	s.mu.Lock()
	s.indexerCaps = caps
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(caps)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	s.sessionMgr.DeleteSession(req.ID)
	logger.Debug("API closing session", "id", req.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil || stream.Username != s.config.GetAdminUsername() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	go func() {

		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Start()
		os.Exit(0)
	}()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "Restarting..."})
}

func (s *Server) handleDownloadLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	logPath := logger.GetCurrentLogPath()
	info, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Log file not found", http.StatusNotFound)
			return
		}
		logger.Error("Log download stat failed", "path", logPath, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !info.Mode().IsRegular() {
		http.Error(w, "Log file not found", http.StatusNotFound)
		return
	}

	filename := filepath.Base(logPath)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	http.ServeFile(w, r, logPath)
}

func (s *Server) handlePersistedStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	resp := persistedStatsResponse{
		Indexers:  []persistence.IndexerMetric{},
	}
	if mgr == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	indexers, err := mgr.GetLatestIndexerMetrics()
	if err != nil {
		logger.Error("GetLatestIndexerMetrics failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	resp.Indexers = indexers
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()
	resp := persistedStatsResponse{
		Indexers:  []persistence.IndexerMetric{},
	}
	if mgr == nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	from, err := parseDateParam(r.URL.Query().Get("from"))
	if err != nil {
		http.Error(w, "Invalid from date (expected YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	to, err := parseDateParam(r.URL.Query().Get("to"))
	if err != nil {
		http.Error(w, "Invalid to date (expected YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	if to != nil {
		endExclusive := to.Add(24 * time.Hour)
		to = &endExclusive
	}
	if from != nil && to != nil && !from.Before(*to) {
		http.Error(w, "Invalid date range", http.StatusBadRequest)
		return
	}

	indexers, err := mgr.GetIndexerMetricsSummary(from, to)
	if err != nil {
		logger.Error("GetIndexerMetricsSummary failed", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	resp.Indexers = indexers

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// parseStatsRange parses from/to query params for the event-based stats
// endpoints. Defaults to the last 30 days when omitted. 'to' is inclusive and
// extended by 24h to an exclusive upper bound, matching handleStatsHistory.
func parseStatsRange(r *http.Request) (*time.Time, *time.Time, error) {
	from, err := parseDateParam(r.URL.Query().Get("from"))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid from date (expected YYYY-MM-DD)")
	}
	to, err := parseDateParam(r.URL.Query().Get("to"))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid to date (expected YYYY-MM-DD)")
	}
	if from == nil && to == nil {
		end := time.Now()
		start := end.AddDate(0, 0, -30)
		return &start, &end, nil
	}
	if to != nil {
		endExclusive := to.Add(24 * time.Hour)
		to = &endExclusive
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, nil, fmt.Errorf("invalid date range")
	}
	return from, to, nil
}

// handleIndexerStats returns per-tracker event-based aggregation over the
// selected [from,to) window, plus time-distribution charts.
func (s *Server) handleIndexerStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	mgr := s.attemptLister
	s.mu.RUnlock()

	rows := []persistence.IndexerStatsRow{}
	var timeDist *persistence.TimeDistributionStats

	if mgr != nil {
		from, to, err := parseStatsRange(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rows, err = mgr.GetIndexerStats(from, to)
		if err != nil {
			logger.Error("GetIndexerStats failed", "err", err)
		}
		timeDist, err = mgr.GetTimeDistribution(from, to)
		if err != nil {
			logger.Error("GetTimeDistribution failed", "err", err)
		}
	}

	writeJSON(w, map[string]any{"indexers": rows, "time_distribution": timeDist})
}

// handleProviderStats returns per-provider event-based aggregation over the
// selected [from,to) window (provider analogue of indexer stats).
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
