package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"seedstream/pkg/core/env"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/paths"
)

const (
	defaultAdminPasswordHash               = "8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918"
	DefaultInternalIndexerTimeoutSeconds   = 5
	DefaultAggregatorIndexerTimeoutSeconds = 10
	DefaultPlaybackStartupTimeoutSeconds   = 5
	MaxPlaybackStartupTimeoutSeconds       = 60
	DefaultSessionTTLMinutes               = 30
	MinSessionTTLMinutes                   = 1
	MaxSessionTTLMinutes                   = 1440
	DefaultSessionPostPlaybackTTLMinutes   = 240
	MinSessionPostPlaybackTTLMinutes       = 1
	MaxSessionPostPlaybackTTLMinutes       = 1440
	CurrentConfigVersion                   = 2
	StreamModelConfigVersion               = 2
	defaultMigratedStreamID                = "default"
	SeriesSearchScopeSeasonEpisode         = "season_episode"
	SeriesSearchScopeSeason                = "season"
	SeriesSearchScopeNone                  = "none"
	legacySeriesSearchScopeEpisodeParam    = "episode_param"
	legacySeriesSearchScopeEpisodeQuery    = "episode_query"
	legacySeriesSearchScopeSeasonParam     = "season_param"
	legacySeriesSearchScopeSeasonQuery     = "season_query"
)

func ptrBool(b bool) *bool { return &b }

func IsAggregatorIndexerType(indexerType string) bool {
	switch strings.ToLower(strings.TrimSpace(indexerType)) {
	case "aggregator", "nzbhydra", "prowlarr":
		return true
	default:
		return false
	}
}

type IndexerSearchConfig struct {
	SearchResultLimit          int     `json:"search_result_limit,omitempty"`
	EnableSeriesSeasonSearch   *bool   `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool   `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool   `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        *string `json:"search_title_language,omitempty"`
	MovieCategories            *string `json:"movie_categories,omitempty"`
	TVCategories               *string `json:"tv_categories,omitempty"`
	ExtraSearchTerms           *string `json:"extra_search_terms,omitempty"`
	DisableIdSearch            *bool   `json:"disable_id_search,omitempty"`
	DisableStringSearch        *bool   `json:"disable_string_search,omitempty"`
}

type SearchQueryConfig struct {
	Name              string `json:"name"`
	SearchMode        string `json:"search_mode,omitempty"`
	SearchResultLimit int    `json:"search_result_limit,omitempty"`
	IncludeYear       *bool  `json:"include_year,omitempty"`
	// Legacy transport-vs-query hint kept only so older local draft configs still load cleanly.
	UseSeasonEpisodeParams     *bool    `json:"use_season_episode_params,omitempty"`
	SeriesSearchScope          string   `json:"series_search_scope,omitempty"`
	EnableSeriesSeasonSearch   *bool    `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool    `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool    `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        string   `json:"search_title_language,omitempty"`
	SearchTitleLanguages       []string `json:"search_title_languages,omitempty"`
	// Legacy year field kept only so older local draft configs still load cleanly.
	LegacyIncludeYearInTextSearch *bool  `json:"include_year_in_text_search,omitempty"`
	MovieCategories               string `json:"movie_categories,omitempty"`
	TVCategories                  string `json:"tv_categories,omitempty"`
	ExtraSearchTerms              string `json:"extra_search_terms,omitempty"`
	DisableIdSearch               *bool  `json:"disable_id_search,omitempty"`
	DisableStringSearch           *bool  `json:"disable_string_search,omitempty"`
}

type IndexerConfig struct {
	Name                   string `json:"name"`
	URL                    string `json:"url"`
	APIKey                 string `json:"api_key"`
	APIPath                string `json:"api_path"`
	Type                   string `json:"type"`
	APIHitsDay             int    `json:"api_hits_day"`
	DownloadsDay           int    `json:"downloads_day"`
	RateLimitRPS           int    `json:"rate_limit_rps,omitempty"`
	TimeoutSeconds         int    `json:"timeout_seconds,omitempty"`
	SearchResultsCacheTime int    `json:"search_results_cache_time,omitempty"`
	Enabled                *bool  `json:"enabled,omitempty"`

	Username string `json:"username"`
	Password string `json:"password"`

	MovieCategories            string `json:"movie_categories,omitempty"`
	TVCategories               string `json:"tv_categories,omitempty"`
	ExtraSearchTerms           string `json:"extra_search_terms,omitempty"`
	SearchResultLimit          int    `json:"search_result_limit,omitempty"`
	EnableSeriesSeasonSearch   *bool  `json:"enable_series_season_search,omitempty"`
	EnableSeriesCompleteSearch *bool  `json:"enable_series_complete_search,omitempty"`
	EnableSeriesPackSearch     *bool  `json:"enable_series_pack_search,omitempty"`
	SearchTitleLanguage        string `json:"search_title_language,omitempty"`
	DisableIdSearch            *bool  `json:"disable_id_search,omitempty"`
	DisableStringSearch        *bool  `json:"disable_string_search,omitempty"`

	// ProxyURL is an optional HTTP or HTTPS proxy for this indexer (http://host:port or https://...).
	// When empty, HTTP_PROXY / HTTPS_PROXY / NO_PROXY apply via the default proxy resolution.
	ProxyURL string `json:"proxy_url,omitempty"`

	// QueryHeader overrides the global indexer_query_header for search and capability requests to this indexer.
	// Some indexers (e.g. SceneNZBs) gate content by User-Agent; leave empty to use the global setting.
	QueryHeader string `json:"query_header,omitempty"`
	// GrabHeader overrides the global indexer_grab_header for NZB download requests to this indexer.
	// Some indexers (e.g. SceneNZBs) return different NZBs depending on the downloader UA; leave empty to use the global setting.
	GrabHeader string `json:"grab_header,omitempty"`

	// HnR (Hit-and-Run) rules — only relevant for Torznab (torrent) indexers.
	// When set, SeedStream will not remove a torrent from qBittorrent until the
	// obligations are satisfied, protecting your account on private trackers.
	// HnRMinSeedHours: minimum time the torrent must be seeding (e.g. 72 for 3 days).
	// HnRMinRatio: minimum upload/download ratio (e.g. 1.0).
	// HnRMode: "any" means either condition clears the obligation (default);
	//          "all" means both seed time AND ratio must be met.
	HnRMinSeedHours float64 `json:"hnr_min_seed_hours,omitempty"`
	HnRMinRatio     float64 `json:"hnr_min_ratio,omitempty"`
	HnRMode         string  `json:"hnr_mode,omitempty"`
}

// ValidateIndexerProxyURL returns nil if raw is empty or a valid http(s) proxy URL.
func ValidateIndexerProxyURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("proxy URL scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("proxy URL must include a host")
	}
	return nil
}

// ValidateIndexerProxyReachable performs a lightweight TCP dial check to ensure
// the proxy endpoint is reachable.
func ValidateIndexerProxyReachable(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return fmt.Errorf("proxy URL must include a host")
	}
	port := strings.TrimSpace(u.Port())
	if port == "" {
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("proxy is unreachable at %s: %w", addr, err)
	}
	_ = conn.Close()
	return nil
}

// RedactProxyURLForAPI strips userinfo from a proxy URL for non-admin API responses.
func RedactProxyURLForAPI(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}

func (ic IndexerConfig) EffectiveTimeoutSeconds() int {
	if ic.TimeoutSeconds > 0 {
		return ic.TimeoutSeconds
	}
	if IsAggregatorIndexerType(ic.Type) {
		return DefaultAggregatorIndexerTimeoutSeconds
	}
	return DefaultInternalIndexerTimeoutSeconds
}

func (ic IndexerConfig) EffectiveTimeout() time.Duration {
	return time.Duration(ic.EffectiveTimeoutSeconds()) * time.Second
}

func normalizePlaybackStartupTimeoutSeconds(timeout int) int {
	if timeout < 1 || timeout > MaxPlaybackStartupTimeoutSeconds {
		return DefaultPlaybackStartupTimeoutSeconds
	}
	return timeout
}

func (c *Config) EffectivePlaybackStartupTimeoutSeconds() int {
	if c != nil {
		return normalizePlaybackStartupTimeoutSeconds(c.PlaybackStartupTimeoutSeconds)
	}
	return DefaultPlaybackStartupTimeoutSeconds
}

func (c *Config) EffectivePlaybackStartupTimeout() time.Duration {
	return time.Duration(c.EffectivePlaybackStartupTimeoutSeconds()) * time.Second
}

func (c *Config) EffectiveFailoverFastMode() bool {
	if c == nil {
		return true
	}
	return c.FailoverFastMode
}

func normalizeSessionTTLMinutes(ttl int) int {
	if ttl < MinSessionTTLMinutes || ttl > MaxSessionTTLMinutes {
		return DefaultSessionTTLMinutes
	}
	return ttl
}

func normalizeSessionPostPlaybackTTLMinutes(ttl int) int {
	if ttl < MinSessionPostPlaybackTTLMinutes || ttl > MaxSessionPostPlaybackTTLMinutes {
		return DefaultSessionPostPlaybackTTLMinutes
	}
	return ttl
}

func (c *Config) EffectiveSessionTTLSeconds() int {
	if c != nil {
		return normalizeSessionTTLMinutes(c.SessionTTLMinutes) * 60
	}
	return DefaultSessionTTLMinutes * 60
}

func (c *Config) EffectiveSessionPostPlaybackTTLSeconds() int {
	if c != nil {
		return normalizeSessionPostPlaybackTTLMinutes(c.SessionPostPlaybackTTLMinutes) * 60
	}
	return DefaultSessionPostPlaybackTTLMinutes * 60
}

func NormalizeSearchTitleLanguage(language string) string {
	trimmed := strings.TrimSpace(language)
	if strings.EqualFold(trimmed, "original") {
		return ""
	}
	return trimmed
}

func NormalizeSearchTitleLanguages(languages []string) []string {
	if len(languages) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(languages))
	seen := make(map[string]bool, len(languages))
	for _, language := range languages {
		value := NormalizeSearchTitleLanguage(language)
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func DefaultIDSearchTitleLanguages() []string {
	return []string{"en-US", ""}
}

func NormalizeSeriesSearchScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case SeriesSearchScopeSeasonEpisode,
		SeriesSearchScopeSeason,
		SeriesSearchScopeNone:
		return strings.ToLower(strings.TrimSpace(scope))
	case legacySeriesSearchScopeEpisodeParam,
		legacySeriesSearchScopeEpisodeQuery:
		return SeriesSearchScopeSeasonEpisode
	case legacySeriesSearchScopeSeasonParam,
		legacySeriesSearchScopeSeasonQuery:
		return SeriesSearchScopeSeason
	}
	return SeriesSearchScopeSeasonEpisode
}

func normalizeSeriesSearchScopeFromLegacy(scope string, useSeasonEpisodeParams *bool) string {
	normalizedScope := strings.ToLower(strings.TrimSpace(scope))
	if normalizedScope != "" {
		return NormalizeSeriesSearchScope(normalizedScope)
	}
	if useSeasonEpisodeParams != nil {
		return SeriesSearchScopeSeasonEpisode
	}
	return normalizedScope
}

func SeriesSearchScopeUsesSeasonParams(scope, searchMode string) bool {
	if !strings.EqualFold(strings.TrimSpace(searchMode), "id") {
		return false
	}
	switch NormalizeSeriesSearchScope(scope) {
	case SeriesSearchScopeSeasonEpisode, SeriesSearchScopeSeason:
		return true
	default:
		return false
	}
}

func SeriesSearchScopeSearchTarget(scope, searchMode, season, episode string) (string, string) {
	if !SeriesSearchScopeUsesSeasonParams(scope, searchMode) {
		return "", ""
	}
	switch NormalizeSeriesSearchScope(scope) {
	case SeriesSearchScopeSeasonEpisode:
		return strings.TrimSpace(season), strings.TrimSpace(episode)
	case SeriesSearchScopeSeason:
		return strings.TrimSpace(season), ""
	default:
		return "", ""
	}
}

func SeriesSearchScopeRequiresValidation(scope string) bool {
	switch NormalizeSeriesSearchScope(scope) {
	case SeriesSearchScopeSeason, SeriesSearchScopeNone:
		return true
	default:
		return false
	}
}

type Config struct {
	ConfigVersion int `json:"config_version,omitempty"`

	Indexers []IndexerConfig `json:"indexers"`

	AddonPort    int    `json:"addon_port"`
	AddonBaseURL string `json:"addon_base_url"`
	LogLevel     string `json:"log_level"`

	AdminUsername           string `json:"admin_username"`
	AdminPasswordHash       string `json:"admin_password_hash"`
	AdminMustChangePassword bool   `json:"admin_must_change_password"`
	AdminToken              string `json:"admin_token"`

	// TorrentClients are download clients (qBittorrent) that receive torrent
	// picks and keep them seeding on a seedbox. One entry per seedbox/member.
	TorrentClients []TorrentClientConfig `json:"torrent_clients,omitempty"`

	TMDBAPIKey         string `json:"tmdb_api_key,omitempty"`
	IndexerQueryHeader string `json:"indexer_query_header,omitempty"`
	IndexerGrabHeader  string `json:"indexer_grab_header,omitempty"`
	IndexerProxyURL    string `json:"indexer_proxy_url,omitempty"`

	TVDBAPIKey string `json:"tvdb_api_key,omitempty"`

	Streams map[string]*StreamEntry `json:"streams,omitempty"`

	MovieSearchQueries  []SearchQueryConfig `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []SearchQueryConfig `json:"series_search_queries,omitempty"`

	// MemoryLimitMB sets a soft limit on total Go heap (runtime/debug.SetMemoryLimit). 0 = no limit.
	// When set, segment cache is automatically 80% of this limit.
	// Use this to stop memory climbing; the runtime will GC more aggressively to stay under the limit.
	MemoryLimitMB int `json:"memory_limit_mb,omitempty"`

	// KeepLogFiles is how many log files to keep (current seedstream.log + rotated seedstream-*.log). Default 9.
	KeepLogFiles int `json:"keep_log_files,omitempty"`

	// PlaybackStartupTimeoutSeconds bounds probe/open work before the first playable response is ready. Default 5.
	PlaybackStartupTimeoutSeconds int `json:"playback_startup_timeout_seconds,omitempty"`
	// SessionTTLMinutes controls how long a deferred/inactive stream session is kept in memory. Default 30.
	SessionTTLMinutes int `json:"session_ttl_minutes,omitempty"`
	// SessionPostPlaybackTTLMinutes controls how long a session stays in memory after playback ends (paused/stopped). Default 240 (4 hours).
	SessionPostPlaybackTTLMinutes int `json:"session_post_playback_ttl_minutes,omitempty"`
	// FailoverFastMode favors quick failover over exhaustive diagnosis. When enabled,
	// playback skips expensive archive checks that can delay startup.
	FailoverFastMode bool `json:"failover_fast_mode"`

	// CerberusBaseURL, when set, points the torrent-health watchdog at a central
	// Cerberus server for community-wide failure reporting and blocklist data.
	// Leave empty for local-only mode (the default).
	CerberusBaseURL string `json:"cerberus_base_url,omitempty"`
	// CerberusAPIKey is the optional bearer token for the central Cerberus server.
	CerberusAPIKey string `json:"cerberus_api_key,omitempty"`

	LoadedPath string `json:"-"`

	ResetLegacyStreamState bool `json:"-"`
}

type StreamEntry struct {
	Username            string                         `json:"username"`
	Token               string                         `json:"token"`
	Order               int                            `json:"order,omitempty"`
	FilterSortingMode   string                         `json:"filter_sorting_mode,omitempty"`
	IndexerMode         string                         `json:"indexer_mode,omitempty"`
	CombineResults      *bool                          `json:"combine_results,omitempty"`
	EnableFailover      *bool                          `json:"enable_failover,omitempty"`
	ResultsMode         string                         `json:"results_mode,omitempty"`
	AutoAddIndexers     *bool                          `json:"auto_add_indexers,omitempty"`
	IndexerSelections   []string                       `json:"indexer_selections,omitempty"`
	IndexerOverrides    map[string]IndexerSearchConfig `json:"indexer_overrides,omitempty"`
	MovieSearchQueries  []string                       `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []string                       `json:"series_search_queries,omitempty"`
	TorrentClient       *TorrentClientConfig           `json:"torrent_client,omitempty"`
	ProwlarrURL         string                         `json:"prowlarr_url,omitempty"`
	ProwlarrAPIKey      string                         `json:"prowlarr_api_key,omitempty"`
	PasswordHash        string                         `json:"password_hash,omitempty"`
	MustChangePassword  bool                           `json:"must_change_password,omitempty"`
}

func (sq *SearchQueryConfig) AsIndexerSearchConfig() *IndexerSearchConfig {
	if sq == nil {
		return nil
	}
	out := &IndexerSearchConfig{
		SearchResultLimit:          sq.SearchResultLimit,
		EnableSeriesSeasonSearch:   sq.EnableSeriesSeasonSearch,
		EnableSeriesCompleteSearch: sq.EnableSeriesCompleteSearch,
		EnableSeriesPackSearch:     sq.EnableSeriesPackSearch,
	}
	mode := strings.ToLower(strings.TrimSpace(sq.SearchMode))
	switch mode {
	case "id":
		disableID := false
		disableString := true
		out.DisableIdSearch = &disableID
		out.DisableStringSearch = &disableString
	case "text":
		disableID := true
		disableString := false
		out.DisableIdSearch = &disableID
		out.DisableStringSearch = &disableString
	default:
		out.DisableIdSearch = sq.DisableIdSearch
		out.DisableStringSearch = sq.DisableStringSearch
	}
	if s := NormalizeSearchTitleLanguage(sq.SearchTitleLanguage); s != "" {
		out.SearchTitleLanguage = &s
	}
	if sq.MovieCategories != "" {
		s := sq.MovieCategories
		out.MovieCategories = &s
	}
	if sq.TVCategories != "" {
		s := sq.TVCategories
		out.TVCategories = &s
	}
	if sq.ExtraSearchTerms != "" {
		s := sq.ExtraSearchTerms
		out.ExtraSearchTerms = &s
	}
	return out
}

func (c *Config) GetSearchQueryByName(contentType, name string) *SearchQueryConfig {
	if c == nil || name == "" {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(name))
	var queries []SearchQueryConfig
	if contentType == "movie" {
		queries = c.MovieSearchQueries
	} else {
		queries = c.SeriesSearchQueries
	}
	for i := range queries {
		if strings.ToLower(strings.TrimSpace(queries[i].Name)) == target {
			return &queries[i]
		}
	}
	return nil
}

func MergeIndexerSearch(ic *IndexerConfig, override *IndexerSearchConfig, global *Config) *IndexerSearchConfig {
	out := &IndexerSearchConfig{}
	const defaultLimit = 0
	out.SearchResultLimit = defaultLimit
	if ic != nil && ic.SearchResultLimit > 0 {
		out.SearchResultLimit = ic.SearchResultLimit
	}
	if override != nil && override.SearchResultLimit > 0 {
		out.SearchResultLimit = override.SearchResultLimit
	}
	seriesSeasonSearch := true
	if ic != nil && ic.EnableSeriesPackSearch != nil {
		seriesSeasonSearch = *ic.EnableSeriesPackSearch
	}
	if ic != nil && ic.EnableSeriesSeasonSearch != nil {
		seriesSeasonSearch = *ic.EnableSeriesSeasonSearch
	}
	if override != nil && override.EnableSeriesPackSearch != nil {
		seriesSeasonSearch = *override.EnableSeriesPackSearch
	}
	if override != nil && override.EnableSeriesSeasonSearch != nil {
		seriesSeasonSearch = *override.EnableSeriesSeasonSearch
	}
	out.EnableSeriesSeasonSearch = &seriesSeasonSearch

	seriesCompleteSearch := true
	if ic != nil && ic.EnableSeriesPackSearch != nil {
		seriesCompleteSearch = *ic.EnableSeriesPackSearch
	}
	if ic != nil && ic.EnableSeriesCompleteSearch != nil {
		seriesCompleteSearch = *ic.EnableSeriesCompleteSearch
	}
	if override != nil && override.EnableSeriesPackSearch != nil {
		seriesCompleteSearch = *override.EnableSeriesPackSearch
	}
	if override != nil && override.EnableSeriesCompleteSearch != nil {
		seriesCompleteSearch = *override.EnableSeriesCompleteSearch
	}
	out.EnableSeriesCompleteSearch = &seriesCompleteSearch
	s := ""
	if ic != nil && ic.SearchTitleLanguage != "" {
		s = ic.SearchTitleLanguage
	}
	if override != nil && override.SearchTitleLanguage != nil {
		s = *override.SearchTitleLanguage
	}
	out.SearchTitleLanguage = &s

	mc := ""
	if ic != nil {
		mc = ic.MovieCategories
	}
	if override != nil && override.MovieCategories != nil {
		mc = *override.MovieCategories
	}
	if mc != "" {
		out.MovieCategories = &mc
	}

	tc := ""
	if ic != nil {
		tc = ic.TVCategories
	}
	if override != nil && override.TVCategories != nil {
		tc = *override.TVCategories
	}
	if tc != "" {
		out.TVCategories = &tc
	}

	et := ""
	if ic != nil {
		et = ic.ExtraSearchTerms
	}
	if override != nil && override.ExtraSearchTerms != nil {
		et = *override.ExtraSearchTerms
	}
	if et != "" {
		out.ExtraSearchTerms = &et
	}

	disableID := false
	if ic != nil && ic.DisableIdSearch != nil {
		disableID = *ic.DisableIdSearch
	}
	if override != nil && override.DisableIdSearch != nil {
		disableID = *override.DisableIdSearch
	}
	out.DisableIdSearch = &disableID

	disableString := false
	if ic != nil && ic.DisableStringSearch != nil {
		disableString = *ic.DisableStringSearch
	}
	if override != nil && override.DisableStringSearch != nil {
		disableString = *override.DisableStringSearch
	}
	out.DisableStringSearch = &disableString

	return out
}

func (c *Config) GetAdminUsername() string {
	if c != nil && c.AdminUsername != "" {
		return c.AdminUsername
	}
	return "admin"
}

func Load() (*Config, error) {

	dataDir := paths.GetDataDir()
	configPath := filepath.Join(dataDir, "config.json")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Warn("Failed to create data directory", "dir", dataDir, "err", err)
	}

	cfg := &Config{
		AddonPort:                     7000,
		AddonBaseURL:                  "http://localhost:7000",
		LogLevel:                      "INFO",
		AdminUsername:                 "admin",
		MemoryLimitMB:                 512,
		KeepLogFiles:                  9,
		PlaybackStartupTimeoutSeconds: DefaultPlaybackStartupTimeoutSeconds,
		SessionTTLMinutes:             DefaultSessionTTLMinutes,
		SessionPostPlaybackTTLMinutes: DefaultSessionPostPlaybackTTLMinutes,
		FailoverFastMode:              true,
		LoadedPath:                    configPath,
	}

	if err := cfg.LoadFile(configPath); err != nil {
		if os.IsNotExist(err) {
			logger.Info("No config found, creating new one", "path", configPath)
		} else {
			logger.Warn("Failed to load config, using defaults", "path", configPath, "err", err)
		}
	} else {
		logger.Info("Loaded configuration", "path", configPath)
	}
	needSave := false
	streamModelUpgrade := cfg.ConfigVersion < StreamModelConfigVersion
	if streamModelUpgrade {
		if len(cfg.Streams) > 0 {
			logger.Warn("Resetting legacy stream entries from config for stream-model upgrade", "count", len(cfg.Streams), "from_version", cfg.ConfigVersion, "to_version", CurrentConfigVersion)
		} else {
			logger.Info("Applying stream-model upgrade defaults", "from_version", cfg.ConfigVersion, "to_version", CurrentConfigVersion)
		}
		cfg.Streams = make(map[string]*StreamEntry)
		cfg.ResetLegacyStreamState = true
		needSave = true
	}
	if cfg.ConfigVersion < CurrentConfigVersion {
		cfg.ConfigVersion = CurrentConfigVersion
		needSave = true
	}
	if cfg.KeepLogFiles < 1 {
		cfg.KeepLogFiles = 9
	}
	if normalized := normalizePlaybackStartupTimeoutSeconds(cfg.PlaybackStartupTimeoutSeconds); normalized != cfg.PlaybackStartupTimeoutSeconds {
		cfg.PlaybackStartupTimeoutSeconds = normalized
		needSave = true
	}
	if normalized := normalizeSessionTTLMinutes(cfg.SessionTTLMinutes); normalized != cfg.SessionTTLMinutes {
		cfg.SessionTTLMinutes = normalized
		needSave = true
	}
	if normalized := normalizeSessionPostPlaybackTTLMinutes(cfg.SessionPostPlaybackTTLMinutes); normalized != cfg.SessionPostPlaybackTTLMinutes {
		cfg.SessionPostPlaybackTTLMinutes = normalized
		needSave = true
	}

	overrides, keys := env.ReadConfigOverrides()
	ApplyEnvOverrides(cfg, overrides, keys)

	if cfg.MigrateLegacyIndexers() {
		needSave = true
	}

	if cfg.backfillLegacySearchQuerySettings() {
		needSave = true
	}
	if streamModelUpgrade && cfg.applyStreamModelUpgradeDefaults() {
		needSave = true
	}

	if cfg.AdminToken == "" {
		bytes := make([]byte, 32)
		if _, err := rand.Read(bytes); err == nil {
			hash := sha256.Sum256(bytes)
			cfg.AdminToken = hex.EncodeToString(hash[:])
			needSave = true
		}
	}
	if cfg.AdminPasswordHash == "" {
		cfg.AdminPasswordHash = defaultAdminPasswordHash
		cfg.AdminMustChangePassword = true
		needSave = true
	}
	if needSave {
		logger.Info("Set default admin token/password in config")
	}

	if err := cfg.Save(); err != nil {
		logger.Warn("Failed to save config on startup", "err", err)
	} else {
		logger.Info("Saved merged configuration", "path", configPath)
	}

	return cfg, nil
}

func (c *Config) applyStreamModelUpgradeDefaults() bool {
	changed := false
	if c.ensureDefaultMigrationSearchQueries() {
		changed = true
	}
	if c.ensureDefaultMigratedStream() {
		changed = true
	}
	return changed
}

func (c *Config) ensureDefaultMigrationSearchQueries() bool {
	changed := false
	if c.ensureMovieSearchQuery(SearchQueryConfig{
		Name:                "DefaultMovieText",
		SearchMode:          "text",
		SearchResultLimit:   0,
		MovieCategories:     "2000",
		IncludeYear:         ptrBool(true),
		SearchTitleLanguage: "",
	}) {
		changed = true
	}
	if c.ensureMovieSearchQuery(SearchQueryConfig{
		Name:                 "DefaultMovieID",
		SearchMode:           "id",
		SearchResultLimit:    0,
		MovieCategories:      "2000",
		IncludeYear:          ptrBool(false),
		SearchTitleLanguages: DefaultIDSearchTitleLanguages(),
	}) {
		changed = true
	}
	if c.ensureSeriesSearchQuery(SearchQueryConfig{
		Name:                "DefaultTVText",
		SearchMode:          "text",
		SearchResultLimit:   0,
		TVCategories:        "5000",
		IncludeYear:         ptrBool(true),
		SeriesSearchScope:   SeriesSearchScopeSeasonEpisode,
		SearchTitleLanguage: "",
	}) {
		changed = true
	}
	if c.ensureSeriesSearchQuery(SearchQueryConfig{
		Name:                 "DefaultTVID",
		SearchMode:           "id",
		SearchResultLimit:    0,
		TVCategories:         "5000",
		IncludeYear:          ptrBool(false),
		SeriesSearchScope:    SeriesSearchScopeSeasonEpisode,
		SearchTitleLanguages: DefaultIDSearchTitleLanguages(),
	}) {
		changed = true
	}
	return changed
}

func backfillLegacySearchQuerySettingsForQuery(query *SearchQueryConfig, isSeries bool) bool {
	if query == nil {
		return false
	}
	changed := false
	if query.IncludeYear == nil {
		switch {
		case query.LegacyIncludeYearInTextSearch != nil:
			query.IncludeYear = ptrBool(*query.LegacyIncludeYearInTextSearch)
		case strings.EqualFold(strings.TrimSpace(query.SearchMode), "id"):
			query.IncludeYear = ptrBool(false)
		default:
			query.IncludeYear = ptrBool(true)
		}
		changed = true
	}
	if query.LegacyIncludeYearInTextSearch != nil {
		query.LegacyIncludeYearInTextSearch = nil
		changed = true
	}
	normalizedSingleLanguage := NormalizeSearchTitleLanguage(query.SearchTitleLanguage)
	if query.SearchTitleLanguage != normalizedSingleLanguage {
		query.SearchTitleLanguage = normalizedSingleLanguage
		changed = true
	}
	normalizedLanguages := NormalizeSearchTitleLanguages(query.SearchTitleLanguages)
	if len(query.SearchTitleLanguages) != len(normalizedLanguages) || strings.Join(query.SearchTitleLanguages, "\x00") != strings.Join(normalizedLanguages, "\x00") {
		query.SearchTitleLanguages = normalizedLanguages
		changed = true
	}
	if strings.EqualFold(strings.TrimSpace(query.SearchMode), "id") && len(query.SearchTitleLanguages) == 0 {
		if query.SearchTitleLanguage == "" {
			query.SearchTitleLanguages = DefaultIDSearchTitleLanguages()
		} else {
			query.SearchTitleLanguages = NormalizeSearchTitleLanguages([]string{query.SearchTitleLanguage})
		}
		changed = true
	}
	if isSeries {
		normalizedScope := normalizeSeriesSearchScopeFromLegacy(query.SeriesSearchScope, query.UseSeasonEpisodeParams)
		if query.SeriesSearchScope != normalizedScope {
			query.SeriesSearchScope = normalizedScope
			changed = true
		}
	} else if query.SeriesSearchScope != "" {
		query.SeriesSearchScope = ""
		changed = true
	}
	if query.UseSeasonEpisodeParams != nil {
		query.UseSeasonEpisodeParams = nil
		changed = true
	}
	return changed
}

func (c *Config) backfillLegacySearchQuerySettings() bool {
	changed := false
	for i := range c.MovieSearchQueries {
		if backfillLegacySearchQuerySettingsForQuery(&c.MovieSearchQueries[i], false) {
			changed = true
		}
	}
	for i := range c.SeriesSearchQueries {
		if backfillLegacySearchQuerySettingsForQuery(&c.SeriesSearchQueries[i], true) {
			changed = true
		}
	}
	return changed
}

func (c *Config) ensureMovieSearchQuery(query SearchQueryConfig) bool {
	for _, existing := range c.MovieSearchQueries {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(query.Name)) {
			return false
		}
	}
	c.MovieSearchQueries = append(c.MovieSearchQueries, query)
	return true
}

func (c *Config) ensureSeriesSearchQuery(query SearchQueryConfig) bool {
	for _, existing := range c.SeriesSearchQueries {
		if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(query.Name)) {
			return false
		}
	}
	c.SeriesSearchQueries = append(c.SeriesSearchQueries, query)
	return true
}

func (c *Config) ensureDefaultMigratedStream() bool {
	if c.Streams == nil {
		c.Streams = make(map[string]*StreamEntry)
	}
	if _, exists := c.Streams[defaultMigratedStreamID]; exists {
		return false
	}
	token, err := generateConfigToken()
	if err != nil {
		logger.Warn("Failed to generate token for migrated default stream", "err", err)
		return false
	}
	c.Streams[defaultMigratedStreamID] = &StreamEntry{
		Username:            defaultMigratedStreamID,
		Token:               token,
		Order:               1,
		FilterSortingMode:   "aiostreams",
		IndexerMode:         "combine",
		CombineResults:      ptrBool(true),
		EnableFailover:      ptrBool(true),
		ResultsMode:         "display_all",
		AutoAddIndexers:     ptrBool(true),
		IndexerOverrides:    make(map[string]IndexerSearchConfig),
		IndexerSelections:   allIndexerNames(c.Indexers),
		MovieSearchQueries:  allSearchQueryNames(c.MovieSearchQueries),
		SeriesSearchQueries: allSearchQueryNames(c.SeriesSearchQueries),
	}
	return true
}

func generateConfigToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

func allIndexerNames(indexers []IndexerConfig) []string {
	names := make([]string, 0, len(indexers))
	for _, indexer := range indexers {
		name := strings.TrimSpace(indexer.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func allSearchQueryNames(queries []SearchQueryConfig) []string {
	names := make([]string, 0, len(queries))
	for _, query := range queries {
		name := strings.TrimSpace(query.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (c *Config) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	type configAlias Config
	var raw struct {
		configAlias
		LegacyDevices map[string]*StreamEntry `json:"devices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*c = Config(raw.configAlias)
	if c.Streams == nil && raw.LegacyDevices != nil {
		c.Streams = raw.LegacyDevices
	}
	c.LoadedPath = path
	return nil
}

func (c *Config) MigrateLegacyIndexers() bool {
	changed := false
	for i := range c.Indexers {
		if c.Indexers[i].Enabled == nil {
			enabled := true
			c.Indexers[i].Enabled = &enabled
			changed = true
		}
	}
	return changed
}

func (c *Config) Save() error {
	path := c.LoadedPath
	if path == "" {
		path = "config.json"
	}
	return c.SaveFile(path)
}

func (c *Config) SaveFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(c); err != nil {
		return err
	}
	c.LoadedPath = path
	return nil
}

func keySet(list []string, s string) bool {
	for _, k := range list {
		if k == s {
			return true
		}
	}
	return false
}

func ApplyEnvOverrides(cfg *Config, o env.ConfigOverrides, keys []string) {
	if keySet(keys, env.KeyAddonPort) {
		cfg.AddonPort = o.AddonPort
	}
	if keySet(keys, env.KeyAddonBaseURL) {
		cfg.AddonBaseURL = o.AddonBaseURL
	}
	if keySet(keys, env.KeyLogLevel) {
		cfg.LogLevel = o.LogLevel
	}
	if keySet(keys, env.KeyKeepLogFiles) {
		cfg.KeepLogFiles = o.KeepLogFiles
	}
	if keySet(keys, env.KeyTMDBAPIKey) {
		cfg.TMDBAPIKey = o.TMDBAPIKey
	}
	if keySet(keys, env.KeyIndexerQueryHeader) {
		cfg.IndexerQueryHeader = o.IndexerQueryHeader
	}
	if keySet(keys, env.KeyIndexerGrabHeader) {
		cfg.IndexerGrabHeader = o.IndexerGrabHeader
	}
	if keySet(keys, env.KeyTVDBAPIKey) {
		cfg.TVDBAPIKey = o.TVDBAPIKey
	}
	if keySet(keys, env.KeyAdminUsername) {
		cfg.AdminUsername = o.AdminUsername
	}
	if keySet(keys, env.KeyAdminMustChangePwd) {
		cfg.AdminMustChangePassword = o.AdminMustChangePwd
	}
	if keySet(keys, env.KeyIndexers) {
		cfg.Indexers = make([]IndexerConfig, len(o.Indexers))
		for i, idx := range o.Indexers {
			enabled := true
			if idx.Enabled != nil {
				enabled = *idx.Enabled
			}
			cfg.Indexers[i] = IndexerConfig{
				Name:    idx.Name,
				URL:     idx.URL,
				APIKey:  idx.APIKey,
				Type:    "torznab",
				Enabled: &enabled,
			}
		}
	}
}

func GetEnvOverrideKeys() []string {
	return env.OverrideKeys()
}

func (c *Config) RedactForAPI() Config {
	out := *c
	out.AdminPasswordHash = ""
	out.AdminToken = ""
	out.IndexerQueryHeader = ""
	out.IndexerGrabHeader = ""
	out.IndexerProxyURL = RedactProxyURLForAPI(c.IndexerProxyURL)
	out.TMDBAPIKey = ""
	out.TVDBAPIKey = ""
	out.Indexers = make([]IndexerConfig, len(c.Indexers))
	for i, indexer := range c.Indexers {
		redactedIndexer := indexer
		redactedIndexer.APIKey = ""
		redactedIndexer.Username = ""
		redactedIndexer.Password = ""
		redactedIndexer.ProxyURL = RedactProxyURLForAPI(indexer.ProxyURL)
		out.Indexers[i] = redactedIndexer
	}
	out.TorrentClients = make([]TorrentClientConfig, len(c.TorrentClients))
	for i, tc := range c.TorrentClients {
		redacted := tc
		redacted.Username = ""
		redacted.Password = ""
		out.TorrentClients[i] = redacted
	}
	return out
}

func CopyEnvOverridesFrom(src, dst *Config) {
	if src == nil || dst == nil {
		return
	}
	keys := env.OverrideKeys()
	for _, k := range keys {
		switch k {
		case env.KeyAddonPort:
			dst.AddonPort = src.AddonPort
		case env.KeyAddonBaseURL:
			dst.AddonBaseURL = src.AddonBaseURL
		case env.KeyLogLevel:
			dst.LogLevel = src.LogLevel
		case env.KeyKeepLogFiles:
			dst.KeepLogFiles = src.KeepLogFiles
		case env.KeyTMDBAPIKey:
			dst.TMDBAPIKey = src.TMDBAPIKey
		case env.KeyIndexerQueryHeader:
			dst.IndexerQueryHeader = src.IndexerQueryHeader
		case env.KeyIndexerGrabHeader:
			dst.IndexerGrabHeader = src.IndexerGrabHeader
		case env.KeyTVDBAPIKey:
			dst.TVDBAPIKey = src.TVDBAPIKey
		case env.KeyAdminUsername:
			dst.AdminUsername = src.AdminUsername
		case env.KeyAdminMustChangePwd:
			dst.AdminMustChangePassword = src.AdminMustChangePassword
		case env.KeyIndexers:
			dst.Indexers = make([]IndexerConfig, len(src.Indexers))
			copy(dst.Indexers, src.Indexers)
		}
	}
}
