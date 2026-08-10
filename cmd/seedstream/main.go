package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/app"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/env"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/initialization"
	"seedstream/pkg/server/api"
	"seedstream/pkg/server/stremio"
	"seedstream/pkg/server/web"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/services/uploadguard"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"

	"github.com/joho/godotenv"
	"golang.org/x/crypto/acme/autocert"
)

var (
	TMDBKey = ""

	TVDBKey = ""

	Version = "dev"
)

func main() {

	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables")
	}

	env.DefaultIndexerUserAgent = "SeedStream/" + Version

	logger.Init(env.LogLevel())

	logger.Info("Starting SeedStream", "version", Version)

	cfg, err := config.Load()
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("configuration error: %w", err))
	}
	logger.SetLevel(cfg.LogLevel)
	logger.PurgeOldLogs(cfg.KeepLogFiles)

	if cfg.MemoryLimitMB > 0 {
		limit := int64(cfg.MemoryLimitMB) * 1024 * 1024
		debug.SetMemoryLimit(limit)
		logger.Info("Go memory limit set", "mb", cfg.MemoryLimitMB)
	}

	userTMDBKey := os.Getenv(env.TMDBAPIKey)
	if userTMDBKey == "" {
		userTMDBKey = strings.TrimSpace(cfg.TMDBAPIKey)
	}
	userTVDBKey := os.Getenv(env.TVDBAPIKey)
	if userTVDBKey == "" {
		userTVDBKey = strings.TrimSpace(cfg.TVDBAPIKey)
	}
	effectiveTMDBKey := userTMDBKey
	if effectiveTMDBKey == "" {
		effectiveTMDBKey = TMDBKey
	}
	effectiveTVDBKey := userTVDBKey
	if effectiveTVDBKey == "" {
		effectiveTVDBKey = TVDBKey
	}
	env.SetRuntimeHeaders(cfg.IndexerQueryHeader, cfg.IndexerGrabHeader)

	dataDir := filepath.Dir(cfg.LoadedPath)
	if dataDir == "" || dataDir == "." {
		dataDir, _ = os.Getwd()
	}

	stateMgr, err := persistence.GetManager(dataDir)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to get state manager: %v", err))
	}

	// Monthly upload guard: one meter shared by the stream server (which records
	// its own HTTP egress and gates heavy titles) and the watchdog (which folds
	// in BitTorrent seeding). A process singleton so both see the same total.
	uploadMeter := uploadguard.New(stateMgr, nil)

	{
		var stateAdmin struct {
			PasswordHash       string `json:"password_hash"`
			MustChangePassword bool   `json:"must_change_password"`
		}
		if found, _ := stateMgr.Get("admin", &stateAdmin); found {
			cfg.AdminPasswordHash = stateAdmin.PasswordHash
			cfg.AdminMustChangePassword = stateAdmin.MustChangePassword
			if _, err := cfg.EnsureBootstrapAdminPassword(); err != nil {
				initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize admin credentials: %w", err))
			}
			if err := cfg.Save(); err != nil {
				logger.Warn("Failed to save config after admin migration", "err", err)
			} else {
				stateMgr.Delete("admin")
				stateMgr.Delete("admin_sessions")
				_ = stateMgr.Flush()
				logger.Info("Migrated admin credentials from state.json to config.json")
			}
		}
	}

	{
		var stateStreams map[string]*auth.Stream
		if found, _ := stateMgr.Get("devices", &stateStreams); found && len(stateStreams) > 0 {
				if cfg.Streams == nil {
					cfg.Streams = make(map[string]*config.StreamEntry)
				}
				for k, stream := range stateStreams {
					if stream == nil {
						continue
					}
					if _, exists := cfg.Streams[k]; exists {
						continue
					}
					ov := stream.IndexerOverrides
					if ov == nil {
						ov = make(map[string]config.IndexerSearchConfig)
					}
					cfg.Streams[k] = &config.StreamEntry{
						Username:            stream.Username,
						Token:               stream.Token,
						Order:               stream.Order,
						FilterSortingMode:   stream.FilterSortingMode,
						IndexerMode:         stream.IndexerMode,
						CombineResults:      stream.CombineResults,
						EnableFailover:      stream.EnableFailover,
						ResultsMode:         stream.ResultsMode,
						AutoAddIndexers:     stream.AutoAddIndexers,
						IndexerOverrides:    ov,
						IndexerSelections:   append([]string(nil), stream.IndexerSelections...),
						MovieSearchQueries:  append([]string(nil), stream.MovieSearchQueries...),
						SeriesSearchQueries: append([]string(nil), stream.SeriesSearchQueries...),
						TorrentClient:       stream.TorrentClient,
						PasswordHash:        stream.PasswordHash,
						MustChangePassword:  stream.MustChangePassword,
					}
				}
				if err := cfg.Save(); err != nil {
					logger.Warn("Failed to save config after streams migration", "err", err)
				} else {
					stateMgr.Delete("devices")
					stateMgr.Delete("users")
					_ = stateMgr.Flush()
					logger.Info("Migrated streams from state.json to config.json")
				}
		}
	}

	application := app.New()
	comp, err := application.Build(cfg, app.BuildOpts{
		TMDBAPIKey:         userTMDBKey,
		TVDBAPIKey:         userTVDBKey,
		FallbackTMDBAPIKey: TMDBKey,
		FallbackTVDBAPIKey: TVDBKey,
		DataDir:            dataDir,
		SessionTTL:         30 * time.Minute,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to build components: %w", err))
	}

	// Say whether metadata lookups can work before anyone tries to stream. A
	// bad or missing key makes every search return an empty list that looks
	// exactly like "nothing found". In the background so a slow TMDB never
	// delays startup.
	go func() {
		checkCtx, cancelCheck := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCheck()
		application.ValidateTMDBKey(checkCtx)
	}()

	var cerberusOpts []cerberus.Option
	if backend := cerberus.NewHTTPBackend(cfg.CerberusBaseURL, cfg.CerberusAPIKey); backend != nil {
		cerberusOpts = append(cerberusOpts, cerberus.WithBackend(backend))
		logger.Info("Cerberus central backend configured", "base_url", cfg.CerberusBaseURL)
	}
	cerberusClient := cerberus.New(stateMgr, cerberusOpts...)

	sessionTTL := time.Duration(cfg.EffectiveSessionTTLSeconds()) * time.Second
	postPlaybackTTL := time.Duration(cfg.EffectiveSessionPostPlaybackTTLSeconds()) * time.Second
	sessionManager := session.NewManager(sessionTTL)
	sessionManager.SetPostPlaybackEvictTTL(postPlaybackTTL)
	logger.Info("Session manager initialized", "ttl", sessionTTL, "post_playback_ttl", postPlaybackTTL)

	saveConfig := func() error { return cfg.Save() }
	streamManager, err := auth.NewStreamManagerFromConfig(cfg, saveConfig)
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize stream manager: %v", err))
	}
	stremioServer, err := stremio.NewServer(&stremio.ServerOptions{
		Config:         comp.Config,
		BaseURL:        comp.Config.AddonBaseURL,
		Port:           comp.Config.AddonPort,
		Indexer:        comp.Indexer,
		SessionManager: sessionManager,
		TriageService:  comp.Triage,
		TMDBClient:     comp.TMDBClient,
		TVDBClient:     comp.TVDBClient,
		StreamManager:  streamManager,
		Version:        Version,
		CerberusClient: cerberusClient,
		UploadMeter:    uploadMeter,
		UsageManager:   comp.UsageManager,
	})
	if err != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("failed to initialize Stremio server: %v", err))
	}

	torrentMgr := torrent.NewManager(comp.Config.TorrentClients)
	watchdog := torrent.NewWatchdog(torrentMgr, cerberusClient, comp.Indexer, comp.Config, uploadMeter)
	if watchdog != nil {
		go watchdog.Start(context.Background(), torrent.WatchdogConfig{})
		logger.Info("Cerberus torrent watchdog enabled")
	} else {
		logger.Debug("Cerberus torrent watchdog disabled (no torrent clients or trackers configured)")
	}

	apiServer := api.NewServerWithApp(comp.Config, sessionManager, stremioServer, comp.Indexer, streamManager, application, effectiveTMDBKey, effectiveTVDBKey)
	apiServer.SetIndexerCaps(comp.IndexerCaps)
	apiServer.SetAttemptLister(stateMgr)
	stremioServer.SetWebHandler(web.Handler())
	stremioServer.SetAPIHandler(apiServer.Handler())

	mux := http.NewServeMux()
	stremioServer.SetupRoutes(mux)

	mux.Handle("/api/", apiServer.Handler())

	addr := fmt.Sprintf(":%d", comp.Config.AddonPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	tlsMode := comp.Config.EffectiveTLSMode()
	if comp.Config.TLSEnabled {
		if err := comp.Config.ValidateTLS(); err != nil {
			initialization.WaitForInputAndExit(fmt.Errorf("HTTPS is enabled but misconfigured: %w", err))
		}
	}
	if mismatch, want := comp.Config.BaseURLSchemeMismatch(); mismatch {
		logger.Warn("Base URL scheme does not match the TLS setting — Stremio will be handed URLs that cannot be fetched",
			"base_url", comp.Config.AddonBaseURL, "expected_scheme", want)
	}
	// Two settings whose absence breaks playback silently: with no base URL the
	// stream URLs come out relative and no player can fetch them, and with no
	// stream token every request 401s while the app looks perfectly healthy.
	// Neither shows up anywhere else in the logs, so they are named here.
	if strings.TrimSpace(comp.Config.AddonBaseURL) == "" {
		logger.Warn("addon_base_url is not set — stream URLs will be derived from each incoming request. Set it in Settings → General to the URL players use to reach SeedStream.")
	}
	for name, stream := range comp.Config.Streams {
		if stream != nil && strings.TrimSpace(stream.Token) == "" {
			logger.Warn("Stream has no access token — every request for it will be rejected until a token is generated",
				"stream", name)
		}
	}

	logger.Info("Stremio addon server starting",
		"base_url", comp.Config.AddonBaseURL, "port", comp.Config.AddonPort, "tls", tlsMode)
	logger.Info("Note: Access requires stream authentication tokens")

	var serveErr error
	switch tlsMode {
	case config.TLSModeAuto:
		domain := strings.TrimSpace(comp.Config.TLSAutoDomain)
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domain),
			Cache:      autocert.DirCache(filepath.Join(dataDir, "certs")),
			Email:      strings.TrimSpace(comp.Config.TLSAutoEmail),
		}
		srv.TLSConfig = mgr.TLSConfig()
		// Let's Encrypt only validates on port 80 (HTTP-01) or 443
		// (TLS-ALPN-01) — the CA dials those ports and nothing else. Serving the
		// addon on another port is fine, but the host must still forward 80 here
		// or issuance silently never completes, which is a confusing failure to
		// debug from the TLS handshake alone. Say so up front.
		if comp.Config.AddonPort != 443 {
			logger.Warn("Automatic certificates require port 80 to reach this host — publish it (e.g. \"80:80\" in docker-compose.yml) and point the domain's DNS here, or issuance will not complete",
				"domain", domain, "addon_port", comp.Config.AddonPort)
		}
		// Also redirects plain HTTP callers to HTTPS, so an http:// stream URL
		// still lands correctly.
		go func() {
			if err := http.ListenAndServe(":80", mgr.HTTPHandler(nil)); err != nil {
				logger.Error("Could not listen on :80 for certificate validation — automatic certificates will not be issued or renewed", "err", err)
			}
		}()
		logger.Info("Requesting automatic certificate", "domain", domain, "cache", filepath.Join(dataDir, "certs"))
		serveErr = srv.ListenAndServeTLS("", "")
	case config.TLSModeFiles:
		logger.Info("Serving HTTPS with configured certificate", "cert", comp.Config.TLSCertFile)
		serveErr = srv.ListenAndServeTLS(comp.Config.TLSCertFile, comp.Config.TLSKeyFile)
	default:
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil {
		initialization.WaitForInputAndExit(fmt.Errorf("server failed: %w", serveErr))
	}
}
