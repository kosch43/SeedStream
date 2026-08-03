package api

import (
	"context"
	"sort"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/indexer"
	"seedstream/pkg/session"
)

type SystemStats struct {
	Timestamp      time.Time                   `json:"timestamp"`
	ActiveStreams  int                         `json:"active_streams"`
	Indexers       []IndexerStats              `json:"indexers"`
	ActiveSessions []session.ActiveSessionInfo `json:"active_sessions"`

	// Live seedbox activity, aggregated across configured torrent clients.
	DownloadSpeedMbps float64 `json:"download_speed_mbps"`
	UploadSpeedMbps   float64 `json:"upload_speed_mbps"`
	ActiveTorrents    int     `json:"active_torrents"`
	TotalTorrents     int     `json:"total_torrents"`
	DownloadedMB      float64 `json:"downloaded_mb"`
	UploadedMB        float64 `json:"uploaded_mb"`
	Seeds             int     `json:"seeds"`
	Peers             int     `json:"peers"`
}

type IndexerStats struct {
	Name                 string  `json:"name"`
	APIHitsLimit         int     `json:"api_hits_limit"`
	APIHitsUsed          int     `json:"api_hits_used"`
	APIHitsRemaining     int     `json:"api_hits_remaining"`
	AllTimeAPIHitsUsed   int     `json:"api_hits_used_all_time"`
	DownloadsLimit       int     `json:"downloads_limit"`
	DownloadsUsed        int     `json:"downloads_used"`
	DownloadsRemaining   int     `json:"downloads_remaining"`
	AllTimeDownloadsUsed int     `json:"downloads_used_all_time"`
	SearchesCount        int     `json:"searches_count"`
	UniqueHitsCount      int64   `json:"unique_hits_count"`
	AvgResponseMS        float64 `json:"avg_response_ms"`
}

func (s *Server) collectStats() SystemStats {
	logger.Trace("collectStats start")
	stats := SystemStats{
		Timestamp: time.Now(),
	}

	if s.indexer != nil {
		uniqueIndexerHits := map[string]int64{}
		if s.strmServer != nil {
			uniqueIndexerHits = s.strmServer.GetUniqueIndexerHits()
		}

		type indexerContainer interface {
			GetIndexers() []indexer.Indexer
		}

		var indexers []indexer.Indexer
		if container, ok := s.indexer.(indexerContainer); ok {
			indexers = container.GetIndexers()
		} else {
			indexers = []indexer.Indexer{s.indexer}
		}

		for _, idx := range indexers {
			usage := idx.GetUsage()
			stats.Indexers = append(stats.Indexers, IndexerStats{
				Name:                 idx.Name(),
				APIHitsLimit:         usage.APIHitsLimit,
				APIHitsUsed:          usage.APIHitsUsed,
				APIHitsRemaining:     usage.APIHitsRemaining,
				AllTimeAPIHitsUsed:   usage.AllTimeAPIHitsUsed,
				DownloadsLimit:       usage.DownloadsLimit,
				DownloadsUsed:        usage.DownloadsUsed,
				DownloadsRemaining:   usage.DownloadsRemaining,
				AllTimeDownloadsUsed: usage.AllTimeDownloadsUsed,
				SearchesCount:        usage.SearchesCount,
				UniqueHitsCount:      uniqueIndexerHits[idx.Name()],
				AvgResponseMS:        usage.AvgResponseMS,
			})
		}
		sort.Slice(stats.Indexers, func(i, j int) bool {
			return stats.Indexers[i].Name < stats.Indexers[j].Name
		})
	}

	stats.ActiveSessions = s.sessionMgr.GetActiveSessions()
	stats.ActiveStreams = len(stats.ActiveSessions)

	if s.strmServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		agg := s.strmServer.TorrentStats(ctx)
		cancel()
		const bitsPerByte = 8
		stats.DownloadSpeedMbps = float64(agg.DownloadSpeedBps) * bitsPerByte / 1e6
		stats.UploadSpeedMbps = float64(agg.UploadSpeedBps) * bitsPerByte / 1e6
		stats.ActiveTorrents = agg.ActiveTorrents
		stats.TotalTorrents = agg.TotalTorrents
		stats.DownloadedMB = float64(agg.DownloadedBytes) / (1024 * 1024)
		stats.UploadedMB = float64(agg.UploadedBytes) / (1024 * 1024)
		stats.Seeds = agg.Seeds
		stats.Peers = agg.Peers
	}

	s.maybePersistMetrics(stats)

	logger.Trace("collectStats done", "sessions", len(stats.ActiveSessions))
	return stats
}

func (s *Server) maybePersistMetrics(stats SystemStats) {
	mgr := s.attemptLister
	if mgr == nil {
		return
	}
	now := time.Now()
	const metricsInterval = 30 * time.Second

	s.metricsMu.Lock()
	if s.metricsInFlight {
		s.metricsMu.Unlock()
		return
	}
	if !s.lastMetricsAt.IsZero() && now.Sub(s.lastMetricsAt) < metricsInterval {
		s.metricsMu.Unlock()
		return
	}
	s.metricsInFlight = true
	s.metricsMu.Unlock()

	indexers := make([]persistence.IndexerMetric, 0, len(stats.Indexers))
	for _, idx := range stats.Indexers {
		indexers = append(indexers, persistence.IndexerMetric{
			CollectedAt:     now,
			IndexerName:     idx.Name,
			APIHitsUsed:     idx.APIHitsUsed,
			APIHitsLimit:    idx.APIHitsLimit,
			DownloadsUsed:   idx.DownloadsUsed,
			DownloadsLimit:  idx.DownloadsLimit,
			SearchesCount:   idx.SearchesCount,
			UniqueHitsCount: idx.UniqueHitsCount,
			AvgResponseMS:   idx.AvgResponseMS,
		})
	}

	if err := mgr.RecordMetricsSnapshot(nil, indexers); err != nil {
		s.metricsMu.Lock()
		s.metricsInFlight = false
		s.metricsMu.Unlock()
		logger.Warn("Failed to persist metrics snapshot", "err", err)
		return
	}

	s.metricsMu.Lock()
	s.lastMetricsAt = now
	s.metricsInFlight = false
	s.metricsMu.Unlock()
}
