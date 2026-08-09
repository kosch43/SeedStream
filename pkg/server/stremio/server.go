package stremio

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer"
	"seedstream/pkg/search/triage"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/services/metadata/tmdb"
	"seedstream/pkg/services/metadata/tvdb"
	"seedstream/pkg/services/uploadguard"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

type Server struct {
	mu                        sync.RWMutex
	statsMu                   sync.RWMutex
	manifest                  *Manifest
	version                   string
	baseURL                   string
	config                    *config.Config
	indexer                   indexer.Indexer
	sessionManager            *session.Manager
	triageService             *triage.Service
	tmdbClient                *tmdb.Client
	tvdbClient                *tvdb.Client
	streamManager             *auth.StreamManager
	torrentManager            *torrent.Manager
	cerberusClient            *cerberus.Client
	uploadMeter               *uploadguard.Meter
	usageManager              *indexer.UsageManager
	runtimeCache              sync.Map // content-id key -> int64 runtime seconds
	playlistCache             sync.Map
	rawSearchCache            sync.Map
	recordedSuccessSessionIDs sync.Map // session ID -> struct{}; record actual playback success only once per stream
	nextReleaseIndex          sync.Map // key: streamToken|key.CacheKey() → *nextReleaseCursor; tracks manual "next" progression
	webHandler                http.Handler
	apiHandler                http.Handler
	uniqueIndexerHits         map[string]int64
}

const FailoverOrderPath = "/failover_order"

type ServerOptions struct {
	Config         *config.Config
	BaseURL        string
	Port           int
	Indexer        indexer.Indexer
	SessionManager *session.Manager
	TriageService  *triage.Service
	TMDBClient     *tmdb.Client
	TVDBClient     *tvdb.Client
	StreamManager  *auth.StreamManager
	Version        string
	CerberusClient *cerberus.Client
	UploadMeter    *uploadguard.Meter
	// UsageManager holds the per-tracker counters shown on the statistics page.
	// nil disables the live counters; the persisted event log is unaffected.
	UsageManager *indexer.UsageManager
}

func NewServer(opts *ServerOptions) (*Server, error) {
	if opts == nil {
		return nil, fmt.Errorf("ServerOptions is required")
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	s := &Server{
		manifest:          NewManifest(version),
		version:           version,
		baseURL:           opts.BaseURL,
		config:            opts.Config,
		indexer:           opts.Indexer,
		sessionManager:    opts.SessionManager,
		triageService:     opts.TriageService,
		tmdbClient:        opts.TMDBClient,
		tvdbClient:        opts.TVDBClient,
		streamManager:     opts.StreamManager,
		uploadMeter:       opts.UploadMeter,
		usageManager:      opts.UsageManager,
		uniqueIndexerHits: make(map[string]int64),
	}
	if opts.Config != nil {
		mgr := torrent.NewManager(opts.Config.TorrentClients)
		mgr.BufferBytes = opts.Config.EffectiveTorrentBufferBytes()
		mgr.PrepareTimeout = opts.Config.EffectiveTorrentPrepareTimeout()
		mgr.MinSeeders = opts.Config.EffectiveMinSeeders()
		s.torrentManager = mgr
	}
	s.cerberusClient = opts.CerberusClient

	if err := s.CheckPort(opts.Port); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Server) CheckPort(port int) error {
	address := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("addon port %d listen check failed: %w", port, err)
	}
	ln.Close()
	return nil
}

func (s *Server) SetWebHandler(h http.Handler) {
	s.webHandler = h
}

func (s *Server) SetAPIHandler(h http.Handler) {
	s.apiHandler = h
}

func (s *Server) ClearSearchCaches() {
	s.playlistCache.Range(func(key, _ interface{}) bool {
		s.playlistCache.Delete(key)
		return true
	})
	s.rawSearchCache.Range(func(key, _ interface{}) bool {
		s.rawSearchCache.Delete(key)
		return true
	})
	s.nextReleaseIndex.Range(func(key, _ interface{}) bool {
		s.nextReleaseIndex.Delete(key)
		return true
	})
	logger.Info("Search caches cleared")
}

func (s *Server) Version() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.version != "" {
		return s.version
	}
	return "dev"
}

func (s *Server) addUniqueIndexerHits(hitsByIndexer map[string]int) {
	if len(hitsByIndexer) == 0 {
		return
	}
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	for name, n := range hitsByIndexer {
		if strings.TrimSpace(name) == "" || n <= 0 {
			continue
		}
		s.uniqueIndexerHits[name] += int64(n)
	}
}

// GetUniqueIndexerHits returns a snapshot copy of uniqueIndexerHits keyed by
// tracker name. The copy is read under statsMu to avoid exposing internal
// mutable state to callers.
func (s *Server) GetUniqueIndexerHits() map[string]int64 {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	out := make(map[string]int64, len(s.uniqueIndexerHits))
	for k, v := range s.uniqueIndexerHits {
		out[k] = v
	}
	return out
}

// TorrentStats returns live aggregate activity across the configured torrent
// clients for the dashboard. Returns a zero value when no client is set up.
func (s *Server) TorrentStats(ctx context.Context) torrent.AggregateStats {
	s.mu.RLock()
	mgr := s.torrentManager
	s.mu.RUnlock()
	if mgr == nil {
		return torrent.AggregateStats{}
	}
	return mgr.AggregateStats(ctx)
}

func (s *Server) Reload(opts *ServerOptions) {
	if opts == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config = opts.Config
	s.baseURL = opts.BaseURL
	if opts.Config != nil {
		mgr := torrent.NewManager(opts.Config.TorrentClients)
		mgr.BufferBytes = opts.Config.EffectiveTorrentBufferBytes()
		mgr.PrepareTimeout = opts.Config.EffectiveTorrentPrepareTimeout()
		mgr.MinSeeders = opts.Config.EffectiveMinSeeders()
		s.torrentManager = mgr
	}
	s.indexer = opts.Indexer
	s.triageService = opts.TriageService
	s.tmdbClient = opts.TMDBClient
	s.tvdbClient = opts.TVDBClient
	s.streamManager = opts.StreamManager
}
