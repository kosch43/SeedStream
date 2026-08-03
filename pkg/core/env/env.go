package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	ADDONPort             = "ADDON_PORT"
	ADDONBaseURL          = "ADDON_BASE_URL"
	LOGLevel              = "LOG_LEVEL"
	KeepLogFiles          = "KEEP_LOG_FILES"
	TMDBAPIKey            = "TMDB_API_KEY"
	TVDBAPIKey            = "TVDB_API_KEY"
	TZVar                 = "TZ"
	IndexerPrefix         = "INDEXER_"
	IndexerQueryHeaderEnv = "INDEXER_QUERY_HEADER"
	IndexerGrabHeaderEnv  = "INDEXER_GRAB_HEADER"
)

const (
	KeyAddonPort          = "addon_port"
	KeyAddonBaseURL       = "addon_base_url"
	KeyLogLevel           = "log_level"
	KeyKeepLogFiles       = "keep_log_files"
	KeyIndexers           = "indexers"
	KeyTMDBAPIKey         = "tmdb_api_key"
	KeyTVDBAPIKey         = "tvdb_api_key"
	KeyIndexerQueryHeader = "indexer_query_header"
	KeyIndexerGrabHeader  = "indexer_grab_header"
	KeyAdminUsername      = "admin_username"
	KeyAdminMustChangePwd = "admin_must_change_password"
)

const AdminUsernameEnv = "ADMIN_USERNAME"
const AdminForcePasswordResetEnv = "ADMIN_FORCE_PASSWORD_RESET"

var DefaultIndexerUserAgent = "SeedStream/dev"
var runtimeHeadersMu sync.RWMutex
var runtimeIndexerQueryHeader = ""
var runtimeIndexerGrabHeader = ""

func TZ() string {
	return os.Getenv(TZVar)
}

func IndexerQueryHeader() string {
	if v := os.Getenv(IndexerQueryHeaderEnv); v != "" {
		return v
	}
	runtimeHeadersMu.RLock()
	defer runtimeHeadersMu.RUnlock()
	if runtimeIndexerQueryHeader != "" {
		return runtimeIndexerQueryHeader
	}
	return DefaultIndexerUserAgent
}

func IndexerGrabHeader() string {
	if v := os.Getenv(IndexerGrabHeaderEnv); v != "" {
		return v
	}
	runtimeHeadersMu.RLock()
	defer runtimeHeadersMu.RUnlock()
	if runtimeIndexerGrabHeader != "" {
		return runtimeIndexerGrabHeader
	}
	return DefaultIndexerUserAgent
}

func SetRuntimeHeaders(indexerQueryHeader, indexerGrabHeader string) {
	runtimeHeadersMu.Lock()
	defer runtimeHeadersMu.Unlock()
	runtimeIndexerQueryHeader = strings.TrimSpace(indexerQueryHeader)
	runtimeIndexerGrabHeader = strings.TrimSpace(indexerGrabHeader)
}

func LogLevel() string {
	if v := os.Getenv(LOGLevel); v != "" {
		return v
	}
	return "INFO"
}

type Indexer struct {
	Name    string
	URL     string
	APIKey  string
	Enabled *bool
}

type ConfigOverrides struct {
	AddonPort          int
	AddonBaseURL       string
	LogLevel           string
	KeepLogFiles       int
	TMDBAPIKey         string
	TVDBAPIKey         string
	IndexerQueryHeader string
	IndexerGrabHeader  string
	AdminUsername      string
	AdminMustChangePwd bool
	Indexers           []Indexer
}

func ReadConfigOverrides() (ConfigOverrides, []string) {
	var o ConfigOverrides
	var keys []string

	if v := os.Getenv(ADDONPort); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			o.AddonPort = port
			keys = append(keys, KeyAddonPort)
		}
	}
	if v := os.Getenv(ADDONBaseURL); v != "" {
		o.AddonBaseURL = v
		keys = append(keys, KeyAddonBaseURL)
	}
	if v := os.Getenv(LOGLevel); v != "" {
		o.LogLevel = v
		keys = append(keys, KeyLogLevel)
	}
	if v := os.Getenv(KeepLogFiles); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			o.KeepLogFiles = n
			keys = append(keys, KeyKeepLogFiles)
		}
	}
	if v := os.Getenv(TMDBAPIKey); v != "" {
		o.TMDBAPIKey = v
		keys = append(keys, KeyTMDBAPIKey)
	}
	if v := os.Getenv(TVDBAPIKey); v != "" {
		o.TVDBAPIKey = v
		keys = append(keys, KeyTVDBAPIKey)
	}
	if v := os.Getenv(IndexerQueryHeaderEnv); v != "" {
		o.IndexerQueryHeader = v
		keys = append(keys, KeyIndexerQueryHeader)
	}
	if v := os.Getenv(IndexerGrabHeaderEnv); v != "" {
		o.IndexerGrabHeader = v
		keys = append(keys, KeyIndexerGrabHeader)
	}
	if v := os.Getenv(AdminUsernameEnv); v != "" {
		o.AdminUsername = v
		keys = append(keys, KeyAdminUsername)
	}
	if v, ok := os.LookupEnv(AdminForcePasswordResetEnv); ok && v != "" && getEnvBool(AdminForcePasswordResetEnv, false) {
		o.AdminMustChangePwd = true
		keys = append(keys, KeyAdminMustChangePwd)
	}
	o.Indexers = readIndexersFromEnv()
	if len(o.Indexers) > 0 {
		keys = append(keys, KeyIndexers)
	}

	return o, keys
}

func OverrideKeys() []string {
	_, keys := ReadConfigOverrides()
	return keys
}

func readIndexersFromEnv() []Indexer {
	var list []Indexer
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("%s%d_", IndexerPrefix, i)
		url := os.Getenv(prefix + "URL")
		if url == "" {
			continue
		}
		enabled := getEnvBool(prefix+"ENABLED", true)
		list = append(list, Indexer{
			Name:    getEnv(prefix+"NAME", fmt.Sprintf("Tracker %d", i)),
			URL:     url,
			APIKey:  os.Getenv(prefix + "API_KEY"),
			Enabled: &enabled,
		})
	}
	return list
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.ToLower(v) == "true" || v == "1"
	}
	return defaultVal
}
