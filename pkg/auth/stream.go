package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/persistence"
)

func ptrBool(v bool) *bool { return &v }

func parseTrailingNumber(value string) (int, bool) {
	value = strings.TrimSpace(value)
	end := len(value)
	for end > 0 && value[end-1] >= '0' && value[end-1] <= '9' {
		end--
	}
	if end == len(value) {
		return 0, false
	}
	n, err := strconv.Atoi(value[end:])
	if err != nil {
		return 0, false
	}
	return n, true
}

type Stream struct {
	Username            string                                `json:"username"`
	Token               string                                `json:"token"`
	Order               int                                   `json:"order,omitempty"`
	FilterSortingMode   string                                `json:"filter_sorting_mode,omitempty"`
	IndexerMode         string                                `json:"indexer_mode,omitempty"`
	CombineResults      *bool                                 `json:"combine_results,omitempty"`
	EnableFailover      *bool                                 `json:"enable_failover,omitempty"`
	ResultsMode         string                                `json:"results_mode,omitempty"`
	AutoAddIndexers     *bool                                 `json:"auto_add_indexers,omitempty"`
	IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
	IndexerSelections   []string                              `json:"indexer_selections,omitempty"`
	MovieSearchQueries  []string                              `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []string                              `json:"series_search_queries,omitempty"`
	TorrentClient       *config.TorrentClientConfig           `json:"torrent_client,omitempty"`
	ProwlarrURL         string                                `json:"prowlarr_url,omitempty"`
	ProwlarrAPIKey      string                                `json:"prowlarr_api_key,omitempty"`
	PasswordHash        string                                `json:"password_hash,omitempty"`
	MustChangePassword  bool                                  `json:"must_change_password,omitempty"`
}

type StreamManager struct {
	mu      sync.RWMutex
	streams map[string]*Stream
	manager *persistence.StateManager
	cfg     *config.Config
	saveFn  func() error
}

var globalStreamManager *StreamManager
var streamManagerMu sync.Mutex

func GetStreamManager(dataDir string) (*StreamManager, error) {
	streamManagerMu.Lock()
	defer streamManagerMu.Unlock()

	if globalStreamManager != nil {
		return globalStreamManager, nil
	}

	manager, err := persistence.GetManager(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get persistence manager: %w", err)
	}

	dm := &StreamManager{
		streams: make(map[string]*Stream),
		manager: manager,
	}

	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load streams: %w", err)
	}

	globalStreamManager = dm
	return dm, nil
}

// NewStreamManagerFromConfig creates the shared stream manager backed by config persistence.
func NewStreamManagerFromConfig(cfg *config.Config, saveFn func() error) (*StreamManager, error) {
	streamManagerMu.Lock()
	defer streamManagerMu.Unlock()

	if globalStreamManager != nil {
		return globalStreamManager, nil
	}

	if cfg.Streams == nil {
		cfg.Streams = make(map[string]*config.StreamEntry)
	}

	dm := &StreamManager{
		streams: make(map[string]*Stream),
		cfg:     cfg,
		saveFn:  saveFn,
	}
	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load streams from config: %w", err)
	}
	globalStreamManager = dm
	return dm, nil
}

func (dm *StreamManager) load() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if dm.cfg != nil {
		removedAdmin := dm.syncStreamsFromConfigLocked()
		if removedAdmin {
			if err := dm.saveLocked(); err != nil {
				logger.Warn("Failed to persist removal of legacy admin stream", "err", err)
			}
		}
		return nil
	}

	var devices map[string]*Stream
	found, err := dm.manager.Get("devices", &devices)
	if err != nil {
		return err
	}
	if !found {
		var users map[string]*Stream
		if found, err := dm.manager.Get("users", &users); found && err == nil {
			devices = users
			dm.manager.Set("devices", devices)
			logger.Info("Migrated legacy stream entries in state.json")
		}
	}
	if devices != nil {
		dm.streams = make(map[string]*Stream)
		for k, d := range devices {
			if d == nil {
				continue
			}
			dm.streams[k] = &Stream{
				Username:            d.Username,
				Token:               d.Token,
				Order:               d.Order,
				FilterSortingMode:   d.FilterSortingMode,
				IndexerMode:         d.IndexerMode,
				CombineResults:      d.CombineResults,
				EnableFailover:      d.EnableFailover,
				ResultsMode:         d.ResultsMode,
				AutoAddIndexers:     d.AutoAddIndexers,
				IndexerOverrides:    d.IndexerOverrides,
				IndexerSelections:   append([]string(nil), d.IndexerSelections...),
				MovieSearchQueries:  append([]string(nil), d.MovieSearchQueries...),
				SeriesSearchQueries: append([]string(nil), d.SeriesSearchQueries...),
				TorrentClient:       d.TorrentClient,
				ProwlarrURL:         d.ProwlarrURL,
				ProwlarrAPIKey:      d.ProwlarrAPIKey,
				PasswordHash:        d.PasswordHash,
				MustChangePassword:  d.MustChangePassword,
			}
			if dm.streams[k].IndexerOverrides == nil {
				dm.streams[k].IndexerOverrides = make(map[string]config.IndexerSearchConfig)
			}
		}
	} else {
		dm.streams = make(map[string]*Stream)
	}
	return nil
}

func (dm *StreamManager) syncStreamsFromConfigLocked() bool {
	dm.streams = make(map[string]*Stream)
	if dm.cfg == nil || dm.cfg.Streams == nil {
		return false
	}
	for k, e := range dm.cfg.Streams {
		if e == nil {
			continue
		}
		ov := e.IndexerOverrides
		if ov == nil {
			ov = make(map[string]config.IndexerSearchConfig)
		}
		dm.streams[k] = &Stream{
			Username:            e.Username,
			Token:               e.Token,
			Order:               e.Order,
			FilterSortingMode:   e.FilterSortingMode,
			IndexerMode:         e.IndexerMode,
			CombineResults:      e.CombineResults,
			EnableFailover:      e.EnableFailover,
			ResultsMode:         e.ResultsMode,
			AutoAddIndexers:     e.AutoAddIndexers,
			IndexerOverrides:    ov,
			IndexerSelections:   append([]string(nil), e.IndexerSelections...),
			MovieSearchQueries:  append([]string(nil), e.MovieSearchQueries...),
			SeriesSearchQueries: append([]string(nil), e.SeriesSearchQueries...),
			TorrentClient:       e.TorrentClient,
			ProwlarrURL:         e.ProwlarrURL,
			ProwlarrAPIKey:      e.ProwlarrAPIKey,
			PasswordHash:        e.PasswordHash,
			MustChangePassword:  e.MustChangePassword,
		}
	}
	return false
}

func (dm *StreamManager) saveLocked() error {
	if dm.cfg != nil {
		dm.cfg.Streams = make(map[string]*config.StreamEntry)
		for k, d := range dm.streams {
			ov := d.IndexerOverrides
			if ov == nil {
				ov = make(map[string]config.IndexerSearchConfig)
			}
			dm.cfg.Streams[k] = &config.StreamEntry{
				Username:            d.Username,
				Token:               d.Token,
				Order:               d.Order,
				FilterSortingMode:   d.FilterSortingMode,
				IndexerMode:         d.IndexerMode,
				CombineResults:      d.CombineResults,
				EnableFailover:      d.EnableFailover,
				ResultsMode:         d.ResultsMode,
				AutoAddIndexers:     d.AutoAddIndexers,
				IndexerOverrides:    ov,
				IndexerSelections:   append([]string(nil), d.IndexerSelections...),
				MovieSearchQueries:  append([]string(nil), d.MovieSearchQueries...),
				SeriesSearchQueries: append([]string(nil), d.SeriesSearchQueries...),
				TorrentClient:       d.TorrentClient,
				ProwlarrURL:         d.ProwlarrURL,
				ProwlarrAPIKey:      d.ProwlarrAPIKey,
				PasswordHash:        d.PasswordHash,
				MustChangePassword:  d.MustChangePassword,
			}
		}
		if dm.saveFn != nil {
			return dm.saveFn()
		}
		return dm.cfg.Save()
	}
	if dm.manager == nil {
		return fmt.Errorf("persistence manager unavailable")
	}
	return dm.manager.Set("devices", dm.streams)
}

func (dm *StreamManager) SetConfig(cfg *config.Config, saveFn func() error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.cfg = cfg
	dm.saveFn = saveFn
	if dm.cfg != nil && dm.cfg.Streams == nil {
		dm.cfg.Streams = make(map[string]*config.StreamEntry)
	}
	if dm.cfg != nil {
		dm.syncStreamsFromConfigLocked()
	}
}

// HashPassword returns a bcrypt hash of the password. bcrypt is used because
// it is slow by design (resistant to brute-force) and embeds its own salt.
func HashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		// Unreachable under normal conditions; fall back to SHA-256 if bcrypt
		// somehow fails so the server doesn't break.
		h := sha256.Sum256([]byte(password))
		return hex.EncodeToString(h[:])
	}
	return string(hash)
}

// CheckPassword verifies a password against either the current bcrypt format or
// the legacy SHA-256 format used by older SeedStream installations.
func CheckPassword(password, hash string) bool {
	return checkPassword(password, hash)
}

// checkPassword verifies password against hash. Handles both the legacy
// unsalted SHA-256 format (hex string) and the current bcrypt format ($2a$/…).
// On a successful legacy-format match the caller should re-hash with HashPassword
// and persist the new hash so the account is silently upgraded.
func checkPassword(password, hash string) bool {
	if strings.HasPrefix(hash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
	// Legacy: unsalted SHA-256
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:]) == hash
}

func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

func (dm *StreamManager) Authenticate(loginUsername, password, adminUsername, adminPasswordHash string, _ ...string) (*Stream, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}

	if strings.EqualFold(strings.TrimSpace(loginUsername), strings.TrimSpace(adminUsername)) {
		if adminPasswordHash == "" {
			return nil, fmt.Errorf("invalid credentials")
		}
		if !checkPassword(password, adminPasswordHash) {
			return nil, fmt.Errorf("invalid credentials")
		}
		// Admin credentials are for the dashboard only. Do not return an addon
		// token from the password authentication path.
		return &Stream{
			Username:         adminUsername,
			IndexerOverrides: nil,
		}, nil
	}

	// Check member streams
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	stream, ok := dm.streams[strings.ToLower(loginUsername)]
	if !ok {
		// also check case-insensitive by iterating
		for _, s := range dm.streams {
			if strings.EqualFold(s.Username, loginUsername) {
				stream = s
				ok = true
				break
			}
		}
	}
	if !ok || stream.PasswordHash == "" {
		return nil, fmt.Errorf("invalid credentials")
	}
	if !checkPassword(password, stream.PasswordHash) {
		return nil, fmt.Errorf("invalid credentials")
	}
	cp := *stream
	return &cp, nil
}

// AuthenticateToken resolves an addon stream token. The optional tokens are
// the legacy AdminToken addon alias and AdminSessionToken respectively. The
// latter is explicitly rejected here, and the legacy alias resolves to the
// configured default stream rather than an admin principal.
func (dm *StreamManager) AuthenticateToken(token string, adminUsername string, compatibilityTokens ...string) (*Stream, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}
	legacyAddonToken := ""
	adminSessionToken := ""
	if len(compatibilityTokens) > 0 {
		legacyAddonToken = compatibilityTokens[0]
	}
	if len(compatibilityTokens) > 1 {
		adminSessionToken = compatibilityTokens[1]
	}
	if token == "" || token == adminSessionToken || (dm.cfg != nil && token == dm.cfg.AdminSessionToken) {
		return nil, fmt.Errorf("invalid token")
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()
	if legacyAddonToken != "" && token == legacyAddonToken {
		_, stream, exists := dm.streamByUsernameLocked("default")
		if exists && stream != nil {
			cp := *stream
			return &cp, nil
		}
		return nil, fmt.Errorf("invalid token")
	}
	for _, stream := range dm.streams {
		if stream != nil && stream.Token != "" && stream.Token == token {
			if dm.cfg != nil && token == dm.cfg.AdminSessionToken {
				return nil, fmt.Errorf("invalid token")
			}
			cp := *stream
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("invalid token")
}

func (dm *StreamManager) GetStream(username string, adminUsername string) (*Stream, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	_, stream, exists := dm.streamByUsernameLocked(username)
	if !exists {
		return nil, fmt.Errorf("stream not found")
	}
	cp := *stream
	return &cp, nil
}

func (dm *StreamManager) GetAllStreams() []Stream {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	streams := make([]Stream, 0, len(dm.streams))
	for _, stream := range dm.streams {
		if stream == nil {
			continue
		}
		streams = append(streams, Stream{
			Username:            stream.Username,
			Token:               stream.Token,
			Order:               stream.Order,
			FilterSortingMode:   stream.FilterSortingMode,
			IndexerMode:         stream.IndexerMode,
			CombineResults:      stream.CombineResults,
			EnableFailover:      stream.EnableFailover,
			ResultsMode:         stream.ResultsMode,
			AutoAddIndexers:     stream.AutoAddIndexers,
			IndexerOverrides:    stream.IndexerOverrides,
			IndexerSelections:   append([]string(nil), stream.IndexerSelections...),
			MovieSearchQueries:  append([]string(nil), stream.MovieSearchQueries...),
			SeriesSearchQueries: append([]string(nil), stream.SeriesSearchQueries...),
		})
	}

	sort.Slice(streams, func(i, j int) bool {
		left := streams[i]
		right := streams[j]

		if left.Order > 0 && right.Order > 0 && left.Order != right.Order {
			return left.Order < right.Order
		}
		if left.Order > 0 && right.Order <= 0 {
			return true
		}
		if left.Order <= 0 && right.Order > 0 {
			return false
		}

		leftNum, leftHasNum := parseTrailingNumber(left.Username)
		rightNum, rightHasNum := parseTrailingNumber(right.Username)
		if leftHasNum && rightHasNum && leftNum != rightNum {
			return leftNum < rightNum
		}
		if !strings.EqualFold(left.Username, right.Username) {
			return strings.ToLower(left.Username) < strings.ToLower(right.Username)
		}
		return left.Username < right.Username
	})

	return streams
}

func (dm *StreamManager) nextStreamOrderLocked() int {
	maxOrder := 0
	for _, stream := range dm.streams {
		if stream != nil && stream.Order > maxOrder {
			maxOrder = stream.Order
		}
	}
	return maxOrder + 1
}

func (dm *StreamManager) CreateStream(username, password string, adminUsername string) (*Stream, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	username = strings.ToLower(strings.TrimSpace(username))
	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == "" {
		return nil, fmt.Errorf("username cannot be empty")
	}
	previousStreams := dm.streams
	if dm.streams == nil {
		dm.streams = make(map[string]*Stream)
	}

	if _, _, exists := dm.streamByUsernameLocked(username); exists {
		return nil, fmt.Errorf("stream already exists")
	}

	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	stream := &Stream{
		Username:            username,
		Token:               token,
		Order:               dm.nextStreamOrderLocked(),
		IndexerMode:         "combine",
		CombineResults:      ptrBool(true),
		AutoAddIndexers:     ptrBool(true),
		IndexerOverrides:    make(map[string]config.IndexerSearchConfig),
		IndexerSelections:   []string{},
		MovieSearchQueries:  []string{},
		SeriesSearchQueries: []string{},
	}

	previousConfigStreams := dm.configStreamsLocked()
	dm.streams[username] = stream

	if err := dm.saveLocked(); err != nil {
		delete(dm.streams, username)
		if previousStreams == nil {
			dm.streams = nil
		}
		dm.restoreConfigStreamsLocked(previousConfigStreams)
		return nil, fmt.Errorf("failed to save stream: %w", err)
	}

	logger.Info("Created stream", "username", username)
	return stream, nil
}

func (dm *StreamManager) RegenerateToken(username string) (string, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	key, stream, exists := dm.streamByUsernameLocked(username)
	if !exists {
		return "", fmt.Errorf("stream not found")
	}

	token, err := GenerateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	previousToken := stream.Token
	previousConfigStreams := dm.configStreamsLocked()
	stream.Token = token

	if err := dm.saveLocked(); err != nil {
		stream.Token = previousToken
		dm.restoreConfigStreamsLocked(previousConfigStreams)
		return "", fmt.Errorf("failed to save stream: %w", err)
	}

	logger.Info("Regenerated token for stream", "username", key)
	return token, nil
}

func (dm *StreamManager) DeleteStream(username string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	key, stream, exists := dm.streamByUsernameLocked(username)
	if !exists {
		return fmt.Errorf("stream not found")
	}

	previousConfigStreams := dm.configStreamsLocked()
	delete(dm.streams, key)

	if err := dm.saveLocked(); err != nil {
		dm.streams[key] = stream
		dm.restoreConfigStreamsLocked(previousConfigStreams)
		return fmt.Errorf("failed to save stream: %w", err)
	}

	logger.Info("Deleted stream", "username", key)
	return nil
}

func (dm *StreamManager) streamByUsernameLocked(username string) (string, *Stream, bool) {
	username = strings.TrimSpace(username)
	if stream, exists := dm.streams[username]; exists {
		return username, stream, stream != nil
	}
	lowerUsername := strings.ToLower(username)
	for key, stream := range dm.streams {
		if stream != nil && strings.EqualFold(stream.Username, username) {
			return key, stream, true
		}
		if strings.EqualFold(key, lowerUsername) {
			return key, stream, stream != nil
		}
	}
	return "", nil, false
}

func (dm *StreamManager) configStreamsLocked() map[string]*config.StreamEntry {
	if dm.cfg == nil {
		return nil
	}
	return dm.cfg.Streams
}

func (dm *StreamManager) restoreConfigStreamsLocked(streams map[string]*config.StreamEntry) {
	if dm.cfg != nil {
		dm.cfg.Streams = streams
	}
}

// UpdateStreamConfig updates only fields managed by the stream assignment UI.
// Tokens, passwords, and per-stream service credentials are untouched.
func (dm *StreamManager) UpdateStreamConfig(username string, streamConfig *Stream) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}
	if streamConfig == nil {
		return fmt.Errorf("stream config is required")
	}

	previous := *stream
	previousConfigStreams := dm.configStreamsLocked()
	stream.FilterSortingMode = strings.TrimSpace(streamConfig.FilterSortingMode)
	stream.IndexerMode = strings.TrimSpace(streamConfig.IndexerMode)
	stream.CombineResults = streamConfig.CombineResults
	stream.EnableFailover = streamConfig.EnableFailover
	stream.ResultsMode = strings.TrimSpace(streamConfig.ResultsMode)
	stream.AutoAddIndexers = streamConfig.AutoAddIndexers
	if streamConfig.IndexerOverrides == nil {
		stream.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		stream.IndexerOverrides = cloneIndexerSearchConfigs(streamConfig.IndexerOverrides)
	}
	stream.IndexerSelections = append([]string(nil), streamConfig.IndexerSelections...)
	stream.MovieSearchQueries = append([]string(nil), streamConfig.MovieSearchQueries...)
	stream.SeriesSearchQueries = append([]string(nil), streamConfig.SeriesSearchQueries...)

	if err := dm.saveLocked(); err != nil {
		*stream = previous
		dm.restoreConfigStreamsLocked(previousConfigStreams)
		return fmt.Errorf("failed to save stream assignments: %w", err)
	}
	return nil
}

func cloneIndexerSearchConfigs(overrides map[string]config.IndexerSearchConfig) map[string]config.IndexerSearchConfig {
	if overrides == nil {
		return nil
	}
	cloned := make(map[string]config.IndexerSearchConfig, len(overrides))
	for name, override := range overrides {
		cloned[name] = override
	}
	return cloned
}

func (dm *StreamManager) UpdateStreamIndexerConfig(username string, selections []string, overrides map[string]config.IndexerSearchConfig) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	if overrides == nil {
		stream.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		stream.IndexerOverrides = overrides
	}
	stream.IndexerSelections = append([]string(nil), selections...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream indexer overrides: %w", err)
	}
	return nil
}

func (dm *StreamManager) UpdateStreamSearchQueries(username string, movieSearchQueries, seriesSearchQueries []string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	stream.MovieSearchQueries = append([]string(nil), movieSearchQueries...)
	stream.SeriesSearchQueries = append([]string(nil), seriesSearchQueries...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream search queries: %w", err)
	}
	return nil
}

func (dm *StreamManager) UpdateStreamGeneralSettings(username, filterSortingMode, indexerMode string, combineResults, enableFailover *bool, resultsMode string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	stream.FilterSortingMode = strings.TrimSpace(filterSortingMode)
	stream.IndexerMode = strings.TrimSpace(indexerMode)
	stream.CombineResults = combineResults
	stream.EnableFailover = enableFailover
	stream.ResultsMode = strings.TrimSpace(resultsMode)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream general settings: %w", err)
	}
	return nil
}

// SetStreamPassword sets a hashed password for a member stream and clears MustChangePassword.
func (dm *StreamManager) SetStreamPassword(username, password string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}
	stream.PasswordHash = HashPassword(password)
	stream.MustChangePassword = false
	return dm.saveLocked()
}
