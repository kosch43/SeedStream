package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/env"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/initialization"
	"seedstream/pkg/search/triage"
	"seedstream/pkg/services/metadata/tmdb"
	"seedstream/pkg/services/metadata/tvdb"
)

type BuildOpts struct {
	TMDBAPIKey string
	TVDBAPIKey string
	// Fallback keys are baked in at link time (-X main.TMDBKey=…) by the
	// release workflow, so official builds work with no key configured. A
	// build from source has no such secret and leaves them empty — which is
	// why ValidateTMDBKey says so at startup rather than letting searches come
	// back empty with no explanation.
	FallbackTMDBAPIKey string
	FallbackTVDBAPIKey string
	DataDir            string
	SessionTTL         time.Duration
}

type Components struct {
	Config      *config.Config
	Indexer     indexer.Indexer
	IndexerCaps map[string]*indexer.Caps
	Triage      *triage.Service
	TMDBClient  *tmdb.Client
	TVDBClient  *tvdb.Client
	// UsageManager holds the per-tracker counters. Carried through so the
	// playback path can count a grab against the tracker that supplied the
	// release; counters only the search path can reach are half a picture.
	UsageManager *indexer.UsageManager
}

type App struct {
	mu         sync.RWMutex
	components *Components
	opts       BuildOpts
}

func resolveDataDir(override, loadedPath string) string {
	dataDir := override
	if dataDir == "" {
		dataDir = filepath.Dir(loadedPath)
	}
	if dataDir == "" || dataDir == "." {
		dataDir, _ = filepath.Abs(".")
	}
	return dataDir
}

func New() *App {
	return &App{}
}

func (a *App) effectiveTMDBKey() string {
	if k := strings.TrimSpace(a.opts.TMDBAPIKey); k != "" {
		return k
	}
	return strings.TrimSpace(a.opts.FallbackTMDBAPIKey)
}

func (a *App) effectiveTVDBKey() string {
	if k := strings.TrimSpace(a.opts.TVDBAPIKey); k != "" {
		return k
	}
	return strings.TrimSpace(a.opts.FallbackTVDBAPIKey)
}

func (a *App) EffectiveTMDBKey() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.effectiveTMDBKey()
}

func (a *App) EffectiveTVDBKey() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.effectiveTVDBKey()
}

func (a *App) Build(cfg *config.Config, opts BuildOpts) (*Components, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts = opts

	comp, err := a.buildFull(cfg, opts)
	if err != nil {
		return nil, err
	}
	a.components = comp
	return comp, nil
}

func (a *App) buildFull(cfg *config.Config, opts BuildOpts) (*Components, error) {
	env.SetRuntimeHeaders(cfg.IndexerQueryHeader, cfg.IndexerGrabHeader)
	base, err := initialization.BuildComponents(cfg)
	if err != nil {
		return nil, err
	}

	triageSvc := triage.NewService()
	dataDir := resolveDataDir(opts.DataDir, cfg.LoadedPath)
	tmdbClient := tmdb.NewClient(a.effectiveTMDBKey())
	tvdbClient := tvdb.NewClient(a.effectiveTVDBKey(), dataDir)

	return &Components{
		Config:       base.Config,
		Indexer:      base.Indexer,
		IndexerCaps:  base.IndexerCaps,
		Triage:       triageSvc,
		TMDBClient:   tmdbClient,
		TVDBClient:   tvdbClient,
		UsageManager: base.UsageManager,
	}, nil
}

// ValidateTMDBKey says out loud, once, whether metadata lookups can work.
//
// Without this a missing or rejected key is silent: every TMDB lookup fails,
// the search pipeline has no title to match releases against, and the addon
// answers {"streams":[]} — which is indistinguishable from "this title has no
// releases". Three rounds of diagnosis went into that gap once already.
//
// Never fatal. TMDB being unreachable for a minute at boot is not a reason to
// refuse to start, and an operator who runs without metadata has been told.
func (a *App) ValidateTMDBKey(ctx context.Context) {
	a.mu.RLock()
	client := (*tmdb.Client)(nil)
	if a.components != nil {
		client = a.components.TMDBClient
	}
	key := a.effectiveTMDBKey()
	a.mu.RUnlock()

	if key == "" {
		logger.Warn("TMDB API key is not set — metadata lookups will fail and searches will return no streams. Set it in Settings → General, the TMDB_API_KEY environment variable, or config.json.")
		return
	}
	if client == nil {
		return
	}
	if err := client.Ping(ctx); err != nil {
		logger.Warn("TMDB API key check failed — if the key was rejected, every search will return no streams", "err", err)
		return
	}
	logger.Info("TMDB API key accepted")
}

type ReloadScope int

const (
	ReloadConfigOnly ReloadScope = iota
	ReloadIndexers
	ReloadFull
)

func ConfigChanged(old, new_ *config.Config) ReloadScope {
	if old == nil || new_ == nil {
		return ReloadFull
	}

	if !reflect.DeepEqual(old.Indexers, new_.Indexers) {
		return ReloadFull
	}
	return ReloadConfigOnly
}

func (a *App) Reload(newCfg *config.Config) (*Components, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.opts.TMDBAPIKey = strings.TrimSpace(newCfg.TMDBAPIKey)
	a.opts.TVDBAPIKey = strings.TrimSpace(newCfg.TVDBAPIKey)
	env.SetRuntimeHeaders(newCfg.IndexerQueryHeader, newCfg.IndexerGrabHeader)

	old := a.components
	scope := ConfigChanged(old.Config, newCfg)

	switch scope {
	case ReloadConfigOnly:

		logger.Info("Reload: config-only - no tracker restart")
		triageSvc := triage.NewService()
		comp := *old
		comp.Config = newCfg
		comp.Triage = triageSvc
		comp.TMDBClient = tmdb.NewClient(a.effectiveTMDBKey())
		dataDir := resolveDataDir(a.opts.DataDir, newCfg.LoadedPath)
		comp.TVDBClient = tvdb.NewClient(a.effectiveTVDBKey(), dataDir)
		a.components = &comp
		return &comp, false, nil

	case ReloadFull:
		logger.Info("Reload: full rebuild (trackers changed)")
		comp, err := a.buildFull(newCfg, a.opts)
		if err != nil {
			return nil, true, err
		}
		a.components = comp
		return comp, true, nil

	default:
		logger.Info("Reload: trackers changed - full rebuild")
		comp, err := a.buildFull(newCfg, a.opts)
		if err != nil {
			return nil, true, err
		}
		a.components = comp
		return comp, true, nil
	}
}

func (a *App) Components() *Components {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.components
}
