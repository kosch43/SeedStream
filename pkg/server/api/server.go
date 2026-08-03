package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/app"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/indexer"
	"seedstream/pkg/server/stremio"
	"seedstream/pkg/session"
)

type Server struct {
	mu            sync.RWMutex
	config        *config.Config
	sessionMgr    *session.Manager
	strmServer    *stremio.Server
	indexer       indexer.Indexer
	indexerCaps   map[string]*indexer.Caps
	streamManager *auth.StreamManager
	app           *app.App

	tmdbAPIKey string
	tvdbAPIKey string

	clients         map[*Client]bool
	clientsMu       sync.Mutex
	logCh           chan string
	attemptLister   *persistence.StateManager
	metricsMu       sync.Mutex
	lastMetricsAt   time.Time
	metricsInFlight bool
}

type Client struct {
	conn   *websocket.Conn
	send   chan WSMessage
	stream *auth.Stream

	user *auth.Stream
}

func NewServer(cfg *config.Config, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer, streamManager *auth.StreamManager, tmdbAPIKey, tvdbAPIKey string) *Server {
	return NewServerWithApp(cfg, sessMgr, strmServer, indexer, streamManager, nil, tmdbAPIKey, tvdbAPIKey)
}

func NewServerWithApp(cfg *config.Config, sessMgr *session.Manager, strmServer *stremio.Server, indexer indexer.Indexer, streamManager *auth.StreamManager, a *app.App, tmdbAPIKey, tvdbAPIKey string) *Server {
	s := &Server{
		config:        cfg,
		sessionMgr:    sessMgr,
		strmServer:    strmServer,
		indexer:       indexer,
		streamManager: streamManager,
		app:           a,
		tmdbAPIKey:    tmdbAPIKey,
		tvdbAPIKey:    tvdbAPIKey,
		clients:       make(map[*Client]bool),
		logCh:         make(chan string, 100),
	}

	logger.SetBroadcast(s.logCh)
	go s.broadcastLogs()

	return s
}

func (s *Server) broadcastLogs() {
	for msgStr := range s.logCh {
		msg := WSMessage{Type: "log_entry", Payload: json.RawMessage(fmt.Sprintf("%q", msgStr))}

		s.clientsMu.Lock()
		for client := range s.clients {
			select {
			case client.send <- msg:
			default:
			}
		}
		s.clientsMu.Unlock()
	}
}

func (s *Server) AddClient(client *Client) {
	s.clientsMu.Lock()
	s.clients[client] = true
	s.clientsMu.Unlock()
}

func (s *Server) RemoveClient(client *Client) {
	s.clientsMu.Lock()
	delete(s.clients, client)
	s.clientsMu.Unlock()
	close(client.send)
}

func (s *Server) SetIndexerCaps(caps map[string]*indexer.Caps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexerCaps = caps
}

func (s *Server) SetAttemptLister(m *persistence.StateManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attemptLister = m
}

func (s *Server) ReloadFromComponents(comp *app.Components, fullReload bool) {
	s.mu.Lock()
	if fullReload {
		s.indexer = comp.Indexer
	}

	s.config = comp.Config
	if s.sessionMgr != nil {
		s.sessionMgr.SetTTL(time.Duration(comp.Config.EffectiveSessionTTLSeconds()) * time.Second)
		s.sessionMgr.SetPostPlaybackEvictTTL(time.Duration(comp.Config.EffectiveSessionPostPlaybackTTLSeconds()) * time.Second)
	}
	s.tmdbAPIKey = strings.TrimSpace(comp.Config.TMDBAPIKey)
	s.tvdbAPIKey = strings.TrimSpace(comp.Config.TVDBAPIKey)
	if s.app != nil {
		s.tmdbAPIKey = s.app.EffectiveTMDBKey()
		s.tvdbAPIKey = s.app.EffectiveTVDBKey()
	}
	if s.streamManager != nil {
		s.streamManager.SetConfig(comp.Config, nil)
	}
	if comp.IndexerCaps != nil {
		s.indexerCaps = comp.IndexerCaps
	}
	s.mu.Unlock()

	if fullReload {
		s.cleanupIndexerUsageFromConfig(comp.Config)
	}

	logger.SetLevel(comp.Config.LogLevel)
	if s.strmServer != nil {
		s.strmServer.Reload(&stremio.ServerOptions{
			Config:        comp.Config,
			BaseURL:       comp.Config.AddonBaseURL,
			Indexer:       comp.Indexer,
			TriageService: comp.Triage,
			TMDBClient:    comp.TMDBClient,
			TVDBClient:    comp.TVDBClient,
			StreamManager: s.streamManager,
		})
	}
}

func (s *Server) cleanupIndexerUsageFromConfig(cfg *config.Config) {
	usageMgr, err := indexer.GetUsageManager(nil)
	if err != nil || usageMgr == nil {
		return
	}
	var configuredNames []string
	if cfg != nil {
		for _, idx := range cfg.Indexers {
			if idx.URL != "" && idx.Name != "" {
				configuredNames = append(configuredNames, idx.Name)
			}
		}
	}
	usageMgr.SyncUsage(configuredNames)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/auth/check", s.handleAuthCheck)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/info", s.handleInfo)

	authMiddleware := auth.StreamAuthMiddleware(s.streamManager, func() string { return s.config.GetAdminUsername() }, func() string { return s.config.AdminToken })
	mux.Handle("/api/ws", authMiddleware(http.HandlerFunc(s.handleWebSocket)))
	mux.Handle("/api/config", authMiddleware(http.HandlerFunc(s.handleConfig)))
	mux.Handle("/api/cache/clear", authMiddleware(http.HandlerFunc(s.handleClearCache)))
	mux.Handle("/api/indexer/caps", authMiddleware(http.HandlerFunc(s.handleGetIndexerCaps)))
	mux.Handle("/api/indexer/caps/refresh", authMiddleware(http.HandlerFunc(s.handleRefreshIndexerCaps)))
	mux.Handle("/api/stats/persisted", authMiddleware(http.HandlerFunc(s.handlePersistedStats)))
	mux.Handle("/api/stats/history", authMiddleware(http.HandlerFunc(s.handleStatsHistory)))
	mux.Handle("/api/stats/indexers", authMiddleware(http.HandlerFunc(s.handleIndexerStats)))
	mux.Handle("/api/sessions/close", authMiddleware(http.HandlerFunc(s.handleCloseSession)))
	mux.Handle("/api/restart", authMiddleware(http.HandlerFunc(s.handleRestart)))
	mux.Handle("/api/auth/change-password", authMiddleware(http.HandlerFunc(s.handleChangePassword)))
	mux.Handle("/api/tmdb/search", authMiddleware(http.HandlerFunc(s.handleTMDBSearch)))
	mux.Handle("/api/tmdb/tv/", authMiddleware(http.HandlerFunc(s.handleTMDBTV)))
	mux.Handle("/api/search/streams", authMiddleware(http.HandlerFunc(s.handleStreams)))
	mux.Handle("/api/search/releases", authMiddleware(http.HandlerFunc(s.handleSearchReleases)))

	mux.Handle("/api/logs/download", authMiddleware(http.HandlerFunc(s.handleDownloadLogs)))

	return mux
}
