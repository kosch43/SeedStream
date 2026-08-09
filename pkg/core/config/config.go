package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"seedstream/pkg/core/env"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/paths"
)

const (
	defaultAdminPasswordHash               = "8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918"
	defaultAdminPassword                   = "admin"
	bootstrapAdminPasswordFile             = "bootstrap-admin-password"
	DefaultInternalIndexerTimeoutSeconds   = 5
	DefaultAggregatorIndexerTimeoutSeconds = 10
	DefaultPlaybackStartupTimeoutSeconds   = 5
	MaxPlaybackStartupTimeoutSeconds       = 60
	DefaultTorrentBufferBytes              = 16 * 1024 * 1024
	DefaultTorrentPrepareTimeoutSeconds    = 90
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

	// DefinitionID selects a bundled tracker definition, which lets SeedStream
	// talk to a private tracker's website directly instead of needing a Torznab
	// service in front of it. When set, Type is "cardigann" and URL is optional:
	// it overrides the definition's own address, which is what keeps a tracker
	// working when it moves to a new domain.
	DefinitionID string `json:"definition_id,omitempty"`
	// DefinitionSettings holds the credentials the chosen definition asks for
	// (username, password, passkey, session cookie and so on), keyed by the
	// setting name the definition declares.
	DefinitionSettings map[string]string `json:"definition_settings,omitempty"`

	// ProxyURL is an optional HTTP or HTTPS proxy for this indexer (http://host:port or https://...).
	// When empty, HTTP_PROXY / HTTPS_PROXY / NO_PROXY apply via the default proxy resolution.
	ProxyURL string `json:"proxy_url,omitempty"`
	// TLSCAFile is an optional PEM file containing additional CA certificates for
	// this indexer. The system trust store is always retained.
	TLSCAFile string `json:"tls_ca_file,omitempty"`
	// TLSInsecureSkipVerify is intentionally unsupported. Keeping the field lets
	// config validation reject unsafe payloads instead of silently accepting them.
	TLSInsecureSkipVerify bool `json:"tls_insecure_skip_verify,omitempty"`

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
	// HnRWindowDays is how long the tracker gives you to discharge the
	// obligation, counted from when the download completed. Without it Cerberus
	// can only report how much has been seeded, never how much time is left —
	// which is the difference between warning you before a breach and noticing
	// after. 0 means the tracker's deadline is unknown.
	HnRWindowDays float64 `json:"hnr_window_days,omitempty"`
	// HnRAllowCleanup opts this tracker in to being considered for automatic
	// cleanup once its obligations are provably met. It must be turned on
	// deliberately, per tracker: an absent or unmatched rule set must never be
	// read as "nothing is owed", which is the mistake that costs an account.
	// Cleanup is evaluation-only today and never removes anything.
	HnRAllowCleanup *bool `json:"hnr_allow_cleanup,omitempty"`
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
	query := u.Query()
	for key := range query {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
		if strings.Contains(normalized, "apikey") || strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") || strings.Contains(normalized, "username") || strings.Contains(normalized, "credential") || normalized == "user" || normalized == "login" || normalized == "auth" || normalized == "key" {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	u.Fragment = ""
	pathParts := strings.Split(u.Path, "/")
	for i, part := range pathParts {
		if isCanonicalTokenSegment(part) {
			pathParts[i] = "[REDACTED]"
		}
	}
	u.Path = strings.Join(pathParts, "/")
	return u.String()
}

func isCanonicalTokenSegment(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
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

// NewIndexerTLSConfig returns the TLS policy used for outbound indexer
// requests. Verification is always enabled; a custom CA extends the system
// roots and never disables normal certificate or hostname checks.
func NewIndexerTLSConfig(caFile string, insecureSkipVerify bool) (*tls.Config, error) {
	if insecureSkipVerify {
		return nil, fmt.Errorf("insecure TLS certificate verification is not supported")
	}

	tlsConfig := &tls.Config{}
	path := strings.TrimSpace(caFile)
	if path == "" {
		return tlsConfig, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("TLS CA file is not readable: %w", err)
	}

	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("TLS CA file contains no valid PEM certificates")
	}
	tlsConfig.RootCAs = roots
	return tlsConfig, nil
}

// TLSClientConfig builds a fresh outbound TLS configuration for this indexer.
func (ic IndexerConfig) TLSClientConfig() (*tls.Config, error) {
	return NewIndexerTLSConfig(ic.TLSCAFile, ic.TLSInsecureSkipVerify)
}

// ValidateTLS checks the per-indexer outbound TLS settings without creating a
// network connection.
func (ic IndexerConfig) ValidateTLS() error {
	_, err := ic.TLSClientConfig()
	return err
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

func (c *Config) EffectiveTorrentBufferBytes() int64 {
	if c != nil && c.TorrentBufferBytes > 0 {
		return c.TorrentBufferBytes
	}
	return DefaultTorrentBufferBytes
}

func (c *Config) EffectiveTorrentPrepareTimeoutSeconds() int {
	if c != nil && c.TorrentPrepareTimeoutSeconds > 0 {
		return c.TorrentPrepareTimeoutSeconds
	}
	return DefaultTorrentPrepareTimeoutSeconds
}

func (c *Config) EffectiveTorrentPrepareTimeout() time.Duration {
	return time.Duration(c.EffectiveTorrentPrepareTimeoutSeconds()) * time.Second
}

func (c *Config) EffectiveFailoverFastMode() bool {
	if c == nil {
		return true
	}
	return c.FailoverFastMode
}

// DefaultCerberusBlocklistDays is how long a torrent stays on the health
// blocklist. Swarms recover, so a permanent ban steadily shrinks what is
// available; expiring entries lets a once-dead torrent be reconsidered.
const DefaultCerberusBlocklistDays = 30

// EffectiveCerberusBlocklistDays returns the blocklist retention in days.
// Negative disables expiry, keeping the old permanent behaviour.
func (c *Config) EffectiveCerberusBlocklistDays() int {
	if c == nil || c.CerberusBlocklistDays == 0 {
		return DefaultCerberusBlocklistDays
	}
	return c.CerberusBlocklistDays
}

// DefaultHnRSafetyMarginPercent is how far past a tracker's stated requirement a
// torrent must seed before its obligation is treated as provably met. Local
// counters and the tracker's own accounting drift, so the margin absorbs that
// difference rather than betting the account on them agreeing exactly.
const DefaultHnRSafetyMarginPercent = 50

// EffectiveHnRSafetyMarginPercent returns the configured margin, never below the
// default — a smaller margin than the default is not accepted, since the whole
// point is to stay clear of the boundary.
func (c *Config) EffectiveHnRSafetyMarginPercent() int {
	if c == nil || c.HnRSafetyMarginPercent < DefaultHnRSafetyMarginPercent {
		return DefaultHnRSafetyMarginPercent
	}
	return c.HnRSafetyMarginPercent
}

// DefaultMinSeeders is the seeder count required of a torrent when the operator
// has not chosen one. Twenty gives enough peers that the probability of a fast
// first-piece arrival is high even with out-of-order swarm delivery.
const DefaultMinSeeders = 20

// EffectiveMinSeeders returns the minimum seeder count to enforce. Unset means
// DefaultMinSeeders; an explicit 0 (or a negative value) disables the check.
func (c *Config) EffectiveMinSeeders() int {
	if c == nil || c.MinSeeders == nil {
		return DefaultMinSeeders
	}
	if *c.MinSeeders < 0 {
		return 0
	}
	return *c.MinSeeders
}

// DefaultPostCapUploadMbps is the assumed post-cap throttle speed when a monthly
// upload cap is configured without an explicit throttle speed.
const DefaultPostCapUploadMbps = 10.0

// UploadGuardEnabled reports whether the monthly-upload guard is active.
func (c *Config) UploadGuardEnabled() bool {
	return c != nil && c.MonthlyUploadCapGB > 0
}

// MonthlyUploadCapBytes converts the configured cap (decimal GB) to bytes.
// Returns 0 when the guard is disabled.
func (c *Config) MonthlyUploadCapBytes() int64 {
	if !c.UploadGuardEnabled() {
		return 0
	}
	return int64(c.MonthlyUploadCapGB * 1e9)
}

// EffectivePostCapUploadMbps returns the post-cap throttle speed, defaulting to
// DefaultPostCapUploadMbps when unset or invalid.
func (c *Config) EffectivePostCapUploadMbps() float64 {
	if c == nil || c.PostCapUploadMbps <= 0 {
		return DefaultPostCapUploadMbps
	}
	return c.PostCapUploadMbps
}

// EffectiveUploadCapResetDay returns the day of month the allowance resets,
// clamped to 1..28 (so it exists in every month) with a default of 1.
func (c *Config) EffectiveUploadCapResetDay() int {
	if c == nil || c.UploadCapResetDay < 1 || c.UploadCapResetDay > 28 {
		return 1
	}
	return c.UploadCapResetDay
}

// DiskGuardEnabled reports whether the integrated Cerberus disk guard is active.
func (c *Config) DiskGuardEnabled() bool {
	return c != nil && c.DiskGuardThresholdPercent > 0
}

// EffectiveDiskGuardThresholdPercent returns a safe filesystem usage threshold.
// Values outside 1..99 disable the guard rather than risking an unexpected
// pause storm from a malformed configuration.
func (c *Config) EffectiveDiskGuardThresholdPercent() int {
	if c == nil || c.DiskGuardThresholdPercent < 1 || c.DiskGuardThresholdPercent > 99 {
		return 0
	}
	return c.DiskGuardThresholdPercent
}

// EffectiveDiskGuardRecoveryPercent leaves a five-point buffer below the stop
// threshold so torrents do not flap around the configured limit.
func (c *Config) EffectiveDiskGuardRecoveryPercent() int {
	threshold := c.EffectiveDiskGuardThresholdPercent()
	if threshold <= 0 {
		return 0
	}
	if threshold <= 5 {
		return 0
	}
	return threshold - 5
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
	// AdminSessionToken authenticates dashboard/API requests only. It must never
	// be emitted in addon URLs or accepted as a stream credential.
	AdminSessionToken string `json:"admin_session_token"`
	// AdminToken is retained as a legacy addon alias. It is not an admin session
	// credential and is resolved only to the configured default stream.
	AdminToken string `json:"admin_token"`

	// TorrentClients are download clients (qBittorrent) that receive torrent
	// picks and keep them seeding on a seedbox. One entry per seedbox/member.
	TorrentClients []TorrentClientConfig `json:"torrent_clients,omitempty"`

	TMDBAPIKey         string `json:"tmdb_api_key,omitempty"`
	IndexerQueryHeader string `json:"indexer_query_header,omitempty"`
	IndexerGrabHeader  string `json:"indexer_grab_header,omitempty"`
	IndexerProxyURL    string `json:"indexer_proxy_url,omitempty"`

	// TLSEnabled makes SeedStream serve HTTPS directly on AddonPort instead of
	// plain HTTP, so torrent streams are encrypted in transit without needing a
	// separate reverse proxy. Leave off when something upstream (Caddy, nginx,
	// Traefik, Cloudflare) already terminates TLS for you.
	TLSEnabled bool `json:"tls_enabled,omitempty"`
	// TLSCertFile and TLSKeyFile are PEM paths for your own certificate, e.g.
	// one issued by certbot or a Cloudflare origin certificate.
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`
	// TLSAutoDomain requests a free Let's Encrypt certificate for this domain
	// instead of using TLSCertFile/TLSKeyFile. The domain must resolve to this
	// host and port 80 must be reachable for the ACME challenge.
	TLSAutoDomain string `json:"tls_auto_domain,omitempty"`
	// TLSAutoEmail is the optional contact address for Let's Encrypt expiry notices.
	TLSAutoEmail string `json:"tls_auto_email,omitempty"`

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
	// TorrentBufferBytes is how much of the target file's head must be on disk
	// before playback starts. Default 16 MiB; lower = faster first frame.
	TorrentBufferBytes int64 `json:"torrent_buffer_bytes,omitempty"`
	// TorrentPrepareTimeoutSeconds bounds how long PrepareForPlayback waits for
	// the head buffer before giving up. Default 90.
	TorrentPrepareTimeoutSeconds int `json:"torrent_prepare_timeout_seconds,omitempty"`
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

	// CerberusBlocklistDays is how long a failed torrent stays blocklisted.
	// 0 uses DefaultCerberusBlocklistDays; a negative value never expires.
	CerberusBlocklistDays int `json:"cerberus_blocklist_days,omitempty"`

	// HnRSafetyMarginPercent is how far past a tracker's stated seed-time
	// requirement a torrent must go before its obligation counts as provably met.
	// Values below DefaultHnRSafetyMarginPercent are raised to it.
	HnRSafetyMarginPercent int `json:"hnr_safety_margin_percent,omitempty"`

	// MinSeeders is the minimum seeder count a torrent must have before
	// SeedStream will offer or play it. A thin swarm downloads too slowly to
	// stream, which is what produces a stall or constant buffering, so such
	// torrents are kept out of the stream list, refused at play time, and
	// treated as stalled sooner by the watchdog.
	//
	// It is only applied where a seeder count is actually known: a tracker that
	// does not publish one is never filtered on it. nil means the default
	// (DefaultMinSeeders); 0 disables the check entirely.
	MinSeeders *int `json:"min_seeders,omitempty"`

	// MonthlyUploadCapGB is the seedbox's monthly upload allowance in gigabytes
	// (decimal, matching how providers quote "2 TB" as 2000 GB). When > 0 the
	// upload guard is active: the watchdog tracks how much has been uploaded this
	// billing period (BitTorrent seeding plus SeedStream's own stream egress) and,
	// once the cap is reached, SeedStream serves only titles whose bitrate fits the
	// post-cap throttle and holds heavier titles with a disclaimer until reset.
	// 0 disables the feature entirely.
	MonthlyUploadCapGB float64 `json:"monthly_upload_cap_gb,omitempty"`
	// PostCapUploadMbps is the upload speed the provider throttles the seedbox to
	// after the monthly cap is hit (e.g. 10). It is the ceiling SeedStream uses to
	// decide which titles can stream without buffering. Default 10 when a cap is set.
	PostCapUploadMbps float64 `json:"post_cap_upload_mbps,omitempty"`
	// UploadCapResetDay is the day of the month (1..28) the upload allowance
	// resets. Default 1.
	UploadCapResetDay int `json:"upload_cap_reset_day,omitempty"`
	// DiskGuardThresholdPercent pauses SeedStream torrents when the local
	// download filesystem reaches this usage percentage. 0 disables the guard.
	// The path is taken from each torrent client's SavePath.
	DiskGuardThresholdPercent int `json:"disk_guard_threshold_percent,omitempty"`

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

	if err := os.MkdirAll(dataDir, 0700); err != nil {
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
			return nil, fmt.Errorf("failed to load existing config %q: %w", configPath, err)
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
		token, err := generateConfigToken()
		if err != nil {
			return nil, fmt.Errorf("failed to generate legacy addon token: %w", err)
		}
		cfg.AdminToken = token
		needSave = true
	}
	if adminSessionTokenConflicts(cfg) {
		token, err := generateConfigToken()
		if err != nil {
			return nil, fmt.Errorf("failed to generate admin session token: %w", err)
		}
		cfg.AdminSessionToken = token
		needSave = true
	}
	if changed, err := cfg.EnsureBootstrapAdminPassword(); err != nil {
		return nil, err
	} else if changed {
		needSave = true
	}
	if needSave {
		logger.Info("Updated generated authentication credentials in config")
	}

	if err := cfg.Save(); err != nil {
		return nil, fmt.Errorf("failed to save configuration on startup: %w", err)
	}
	logger.Info("Saved merged configuration", "path", configPath)

	return cfg, nil
}

func adminSessionTokenConflicts(cfg *Config) bool {
	if cfg == nil || strings.TrimSpace(cfg.AdminSessionToken) == "" || cfg.AdminSessionToken == cfg.AdminToken {
		return true
	}
	for _, stream := range cfg.Streams {
		if stream != nil && stream.Token != "" && stream.Token == cfg.AdminSessionToken {
			return true
		}
	}
	return false
}

// EnsureBootstrapAdminPassword replaces a missing or known default admin
// password with a fresh bcrypt credential and writes the one-time retrieval
// file beside config.json. It leaves every non-default password hash intact.
func (c *Config) EnsureBootstrapAdminPassword() (bool, error) {
	if c == nil || (!isDefaultAdminPasswordHash(c.AdminPasswordHash) && c.AdminPasswordHash != "") {
		return false, nil
	}
	dir := filepath.Dir(c.LoadedPath)
	if dir == "" || dir == "." {
		dir = paths.GetDataDir()
	}
	path := filepath.Join(dir, bootstrapAdminPasswordFile)
	password, err := readBootstrapAdminPassword(path)
	if err != nil {
		password, err = writeBootstrapAdminPassword(path)
		if err != nil {
			return false, fmt.Errorf("failed to create bootstrap admin password: %w", err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, fmt.Errorf("failed to hash bootstrap admin password: %w", err)
	}
	c.AdminPasswordHash = string(hash)
	c.AdminMustChangePassword = true
	logger.Info("Generated bootstrap admin password", "path", path, "instruction", "retrieve the password from this file and remove it after changing the admin password")
	return true, nil
}

// RemoveBootstrapAdminPassword removes the one-time bootstrap secret after a
// successful password change. A missing file is already-clean state.
func (c *Config) RemoveBootstrapAdminPassword() error {
	if c == nil {
		return nil
	}
	dir := filepath.Dir(c.LoadedPath)
	if dir == "" || dir == "." {
		dir = paths.GetDataDir()
	}
	err := os.Remove(filepath.Join(dir, bootstrapAdminPasswordFile))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func readBootstrapAdminPassword(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	password := strings.TrimSpace(string(data))
	if password == "" {
		return "", fmt.Errorf("bootstrap password file is empty")
	}
	return password, nil
}

func isDefaultAdminPasswordHash(hash string) bool {
	hash = strings.TrimSpace(hash)
	if hash == "" || hash == defaultAdminPasswordHash || hash == defaultAdminPassword {
		return true
	}
	return strings.HasPrefix(hash, "$2") && bcrypt.CompareHashAndPassword([]byte(hash), []byte(defaultAdminPassword)) == nil
}

func writeBootstrapAdminPassword(path string) (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	password := hex.EncodeToString(random)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return "", err
	}
	if _, err := file.WriteString(password + "\n"); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return password, nil
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
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if object == nil {
		return fmt.Errorf("configuration must be a JSON object")
	}
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
	if existing, err := existingConfigForSave(path); err != nil {
		return err
	} else if existing != nil {
		// Config update payloads intentionally omit authentication secrets. Keep
		// the on-disk values when a caller saves such a payload directly.
		if c.AdminPasswordHash == "" {
			c.AdminPasswordHash = existing.AdminPasswordHash
		}
		if c.AdminSessionToken == "" {
			c.AdminSessionToken = existing.AdminSessionToken
		}
		if c.AdminToken == "" {
			c.AdminToken = existing.AdminToken
		}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	removeTemp = false
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	if syncErr := directory.Sync(); syncErr != nil {
		_ = directory.Close()
		return syncErr
	}
	if closeErr := directory.Close(); closeErr != nil {
		return closeErr
	}
	c.LoadedPath = path
	return nil
}

func existingConfigForSave(path string) (*Config, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var existing Config
	if err := existing.LoadFile(path); err != nil {
		return nil, fmt.Errorf("refusing to replace malformed existing config %q: %w", path, err)
	}
	return &existing, nil
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
	if c == nil {
		return Config{}
	}
	out := *c
	out.AdminPasswordHash = ""
	out.AdminSessionToken = ""
	out.AdminToken = ""
	out.IndexerQueryHeader = ""
	out.IndexerGrabHeader = ""
	out.IndexerProxyURL = RedactProxyURLForAPI(c.IndexerProxyURL)
	out.AddonBaseURL = RedactProxyURLForAPI(c.AddonBaseURL)
	out.TMDBAPIKey = ""
	out.TVDBAPIKey = ""
	out.CerberusAPIKey = ""
	out.CerberusBaseURL = RedactProxyURLForAPI(c.CerberusBaseURL)
	out.TLSCertFile = ""
	out.TLSKeyFile = ""
	out.Indexers = make([]IndexerConfig, len(c.Indexers))
	for i, indexer := range c.Indexers {
		redactedIndexer := indexer
		redactedIndexer.APIKey = ""
		redactedIndexer.Username = ""
		redactedIndexer.Password = ""
		redactedIndexer.URL = RedactProxyURLForAPI(indexer.URL)
		redactedIndexer.ProxyURL = RedactProxyURLForAPI(indexer.ProxyURL)
		redactedIndexer.QueryHeader = ""
		redactedIndexer.GrabHeader = ""
		redactedIndexer.DefinitionSettings = nil
		out.Indexers[i] = redactedIndexer
	}
	out.TorrentClients = make([]TorrentClientConfig, len(c.TorrentClients))
	for i, tc := range c.TorrentClients {
		redacted := tc
		redacted.Username = ""
		redacted.Password = ""
		redacted.URL = RedactProxyURLForAPI(tc.URL)
		out.TorrentClients[i] = redacted
	}
	if c.Streams != nil {
		out.Streams = make(map[string]*StreamEntry, len(c.Streams))
		for key, entry := range c.Streams {
			if entry == nil {
				continue
			}
			redacted := *entry
			redacted.Token = ""
			redacted.ProwlarrAPIKey = ""
			redacted.PasswordHash = ""
			redacted.ProwlarrURL = RedactProxyURLForAPI(entry.ProwlarrURL)
			if entry.TorrentClient != nil {
				torrentClient := *entry.TorrentClient
				torrentClient.Username = ""
				torrentClient.Password = ""
				torrentClient.URL = RedactProxyURLForAPI(entry.TorrentClient.URL)
				redacted.TorrentClient = &torrentClient
			}
			if entry.IndexerOverrides != nil {
				redacted.IndexerOverrides = make(map[string]IndexerSearchConfig, len(entry.IndexerOverrides))
				for name, override := range entry.IndexerOverrides {
					redacted.IndexerOverrides[name] = override
				}
			}
			redacted.IndexerSelections = append([]string(nil), entry.IndexerSelections...)
			redacted.MovieSearchQueries = append([]string(nil), entry.MovieSearchQueries...)
			redacted.SeriesSearchQueries = append([]string(nil), entry.SeriesSearchQueries...)
			out.Streams[key] = &redacted
		}
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

// TLSMode describes how SeedStream should obtain its certificate.
type TLSMode string

const (
	// TLSModeOff serves plain HTTP (default).
	TLSModeOff TLSMode = "off"
	// TLSModeFiles uses an operator-supplied certificate and key.
	TLSModeFiles TLSMode = "files"
	// TLSModeAuto requests a Let's Encrypt certificate via ACME.
	TLSModeAuto TLSMode = "auto"
)

// EffectiveTLSMode reports how TLS is configured. A configured auto domain wins
// over cert files so a half-filled config resolves predictably.
func (c *Config) EffectiveTLSMode() TLSMode {
	if c == nil || !c.TLSEnabled {
		return TLSModeOff
	}
	if strings.TrimSpace(c.TLSAutoDomain) != "" {
		return TLSModeAuto
	}
	if strings.TrimSpace(c.TLSCertFile) != "" && strings.TrimSpace(c.TLSKeyFile) != "" {
		return TLSModeFiles
	}
	return TLSModeOff
}

// ValidateTLS reports why TLS cannot start, or nil when the settings are usable.
// Returns nil when TLS is switched off.
func (c *Config) ValidateTLS() error {
	if c == nil || !c.TLSEnabled {
		return nil
	}
	if strings.TrimSpace(c.TLSAutoDomain) != "" {
		domain := strings.TrimSpace(c.TLSAutoDomain)
		if strings.Contains(domain, "/") || strings.Contains(domain, ":") {
			return fmt.Errorf("automatic certificate domain must be a bare hostname (got %q)", domain)
		}
		if !strings.Contains(domain, ".") {
			return fmt.Errorf("automatic certificate domain must be a public domain name (got %q)", domain)
		}
		return nil
	}
	certFile := strings.TrimSpace(c.TLSCertFile)
	keyFile := strings.TrimSpace(c.TLSKeyFile)
	if certFile == "" || keyFile == "" {
		return fmt.Errorf("HTTPS is enabled but no certificate is configured: set a certificate and key, or an automatic certificate domain")
	}
	if _, err := os.Stat(certFile); err != nil {
		return fmt.Errorf("certificate file not readable: %w", err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		return fmt.Errorf("certificate key file not readable: %w", err)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return fmt.Errorf("certificate and key could not be loaded as a pair: %w", err)
	}
	return nil
}

// BaseURLSchemeMismatch reports whether AddonBaseURL disagrees with the TLS
// setting. Getting this wrong is the most common way to end up with streams
// Stremio cannot fetch, so it is surfaced as a startup warning.
func (c *Config) BaseURLSchemeMismatch() (mismatch bool, want string) {
	if c == nil {
		return false, ""
	}
	base := strings.TrimSpace(c.AddonBaseURL)
	if base == "" {
		return false, ""
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" {
		return false, ""
	}
	servingTLS := c.EffectiveTLSMode() != TLSModeOff
	if servingTLS && strings.EqualFold(u.Scheme, "http") {
		return true, "https"
	}
	// Only flag plain-http serving when nothing upstream could be terminating
	// TLS for us; an https base URL with TLS off is the normal reverse-proxy setup.
	return false, ""
}
