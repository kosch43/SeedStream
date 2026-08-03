package initialization

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/paths"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/indexer"
	"seedstream/pkg/indexer/newznab"
	"seedstream/pkg/stats"
)

type InitializedComponents struct {
	Config      *config.Config
	Indexer     indexer.Indexer
	IndexerCaps map[string]*indexer.Caps
}

func WaitForInputAndExit(err error) {
	logger.Error("CRITICAL ERROR", "err", err)
	fmt.Println("\nPress Enter to exit...")
	var input string
	fmt.Scanln(&input)
	os.Exit(1)
}

func Bootstrap() (*InitializedComponents, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}
	return BuildComponents(cfg)
}

func BuildComponents(cfg *config.Config) (*InitializedComponents, error) {
	var indexers []indexer.Indexer

	dataDir := paths.GetDataDir()
	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		logger.Error("Failed to initialize state manager", "err", err)
	}
	if stateMgr != nil {
		// Install the event-based statistics recorder so call sites can record
		// search/download events via stats.Default() without plumbing.
		stats.SetDefault(stats.NewSQLiteRecorder(stateMgr))
	}
	if stateMgr != nil && cfg.ResetLegacyStreamState {
		if err := stateMgr.Delete("devices"); err != nil {
			logger.Warn("Failed to clear legacy devices state during stream-model upgrade", "err", err)
		}
		if err := stateMgr.Delete("users"); err != nil {
			logger.Warn("Failed to clear legacy users state during stream-model upgrade", "err", err)
		}
		logger.Info("Cleared legacy persisted device state for stream-model upgrade")
	}

	usageMgr, err := indexer.GetUsageManager(stateMgr)
	if err != nil {
		logger.Error("Failed to initialize usage manager", "err", err)
	}

	for _, idxCfg := range cfg.Indexers {
		if idxCfg.URL == "" {
			continue
		}
		if idxCfg.Enabled != nil && !*idxCfg.Enabled {
			continue
		}

		// SeedStream is torrent-only: only Torznab trackers are initialized.
		// Legacy non-torrent indexer entries are skipped with a warning so old
		// configs keep loading without streaming from them.
		if !config.IsTorrentIndexerType(idxCfg.Type) {
			logger.Warn("Skipping non-torrent indexer (SeedStream only streams torrents)",
				"name", idxCfg.Name, "type", idxCfg.Type)
			continue
		}

		effectiveCfg := idxCfg
		effectiveProxyURL := strings.TrimSpace(idxCfg.ProxyURL)
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.IndexerProxyURL)
		}
		effectiveCfg.ProxyURL = effectiveProxyURL
		client := newznab.NewClient(effectiveCfg, usageMgr)
		indexers = append(indexers, client)
		logger.Info("Initialized Torznab tracker", "name", idxCfg.Name, "url", idxCfg.URL)
	}

	if len(indexers) == 0 {
		logger.Warn("!! No torrent trackers configured. Add some via the web UI or config.json !!")
	}

	aggregator := indexer.NewAggregator(indexers...)

	indexerCaps := make(map[string]*indexer.Caps)
	var capsMu sync.Mutex
	var capsWg sync.WaitGroup
	for _, idx := range indexers {
		if c, ok := idx.(indexer.IndexerWithCaps); ok {
			capsWg.Add(1)
			go func(name string, capsFetcher indexer.IndexerWithCaps) {
				defer capsWg.Done()
				caps, err := capsFetcher.GetCaps()
				if err != nil {
					logger.Warn("Failed to fetch caps", "indexer", name, "err", err)
					return
				}
				capsMu.Lock()
				indexerCaps[name] = caps
				capsMu.Unlock()
			}(idx.Name(), c)
		}
	}
	capsWg.Wait()
	if len(indexerCaps) > 0 {
		logger.Info("Fetched tracker capabilities", "count", len(indexerCaps))
	}

	return &InitializedComponents{
		Config:      cfg,
		Indexer:     aggregator,
		IndexerCaps: indexerCaps,
	}, nil
}
