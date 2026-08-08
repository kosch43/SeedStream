package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
)

const apiStreamsPrefix = "/api/streams"

// streamAPIResponse deliberately lists the fields that are safe for the
// administrator UI. Do not embed auth.Stream here: it contains passwords and
// per-stream service credentials that must never be serialized by this API.
type streamAPIResponse struct {
	Username            string                                `json:"username"`
	Token               string                                `json:"token"`
	ManifestURL         string                                `json:"manifest_url"`
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
	TorrentClient       *streamTorrentClientResponse          `json:"torrent_client,omitempty"`
	ProwlarrURL         string                                `json:"prowlarr_url,omitempty"`
}

// streamTorrentClientResponse excludes username/password and also removes
// userinfo from URL values, which can contain credentials independently of the
// struct fields.
type streamTorrentClientResponse struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	URL        string `json:"url"`
	Category   string `json:"category,omitempty"`
	SavePath   string `json:"save_path,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

type streamAssignmentsRequest struct {
	FilterSortingMode   string                                `json:"filter_sorting_mode"`
	IndexerMode         string                                `json:"indexer_mode"`
	CombineResults      *bool                                 `json:"combine_results"`
	EnableFailover      *bool                                 `json:"enable_failover"`
	ResultsMode         string                                `json:"results_mode"`
	AutoAddIndexers     *bool                                 `json:"auto_add_indexers"`
	IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
	IndexerSelections   []string                              `json:"indexer_selections"`
	MovieSearchQueries  []string                              `json:"movie_search_queries"`
	SeriesSearchQueries []string                              `json:"series_search_queries"`
}

func (s *Server) adminUsername() string {
	if s.config == nil {
		return "admin"
	}
	return s.config.GetAdminUsername()
}

func (s *Server) requireStreamsAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !auth.IsAdminContext(r) {
		http.Error(w, "Only admin can manage streams", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) manifestURL(token string) string {
	if s.config == nil || strings.TrimSpace(token) == "" {
		return ""
	}
	baseURL := strings.TrimRight(redactStreamURL(s.config.AddonBaseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + "/" + url.PathEscape(token) + "/manifest.json"
}

func redactStreamURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
		if strings.Contains(normalized, "apikey") || strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") || strings.Contains(normalized, "username") || strings.Contains(normalized, "credential") || normalized == "user" || normalized == "login" || normalized == "auth" || normalized == "key" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	return parsed.String()
}

func streamTorrentClientResponseFor(tc *config.TorrentClientConfig) *streamTorrentClientResponse {
	if tc == nil {
		return nil
	}
	return &streamTorrentClientResponse{
		Name:       tc.Name,
		Type:       tc.Type,
		URL:        redactStreamURL(tc.URL),
		Category:   tc.Category,
		SavePath:   tc.SavePath,
		RemotePath: tc.RemotePath,
		Enabled:    tc.Enabled,
	}
}

func (s *Server) streamAPIResponse(stream *auth.Stream) streamAPIResponse {
	if stream == nil {
		return streamAPIResponse{}
	}
	return streamAPIResponse{
		Username:            stream.Username,
		Token:               stream.Token,
		ManifestURL:         s.manifestURL(stream.Token),
		Order:               stream.Order,
		FilterSortingMode:   stream.FilterSortingMode,
		IndexerMode:         stream.IndexerMode,
		CombineResults:      stream.CombineResults,
		EnableFailover:      stream.EnableFailover,
		ResultsMode:         stream.ResultsMode,
		AutoAddIndexers:     stream.AutoAddIndexers,
		IndexerOverrides:    cloneIndexerOverrides(stream.IndexerOverrides),
		IndexerSelections:   append([]string(nil), stream.IndexerSelections...),
		MovieSearchQueries:  append([]string(nil), stream.MovieSearchQueries...),
		SeriesSearchQueries: append([]string(nil), stream.SeriesSearchQueries...),
		TorrentClient:       streamTorrentClientResponseFor(stream.TorrentClient),
		ProwlarrURL:         redactStreamURL(stream.ProwlarrURL),
	}
}

func writeStreamJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(value)
}

func writeStreamError(w http.ResponseWriter, status int, err error) {
	message := "stream request failed"
	if err != nil {
		message = err.Error()
	}
	writeStreamJSON(w, status, map[string]string{"error": message})
}

func (s *Server) handleManagedStreams(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, apiStreamsPrefix)
	if path == "" || path == "/" {
		switch r.Method {
		case http.MethodGet:
			s.handleStreamsList(w, r)
		case http.MethodPost:
			s.handleStreamsCreate(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if strings.Trim(path, "/") == "configs" {
		s.handlePutStreamConfigs(w, r)
		return
	}

	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 && parts[0] != "" {
		s.handleStreamByUsername(w, r)
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "regenerate-token" {
		s.handleStreamByUsername(w, r)
		return
	}
	http.Error(w, "Not found", http.StatusNotFound)
}

func (s *Server) handleStreamsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireStreamsAdmin(w, r) {
		return
	}
	if s.streamManager == nil {
		http.Error(w, "Stream manager unavailable", http.StatusServiceUnavailable)
		return
	}

	streams := s.streamManager.GetAllStreams()
	responses := make([]streamAPIResponse, 0, len(streams))
	for i := range streams {
		stream, err := s.streamManager.GetStream(streams[i].Username, s.adminUsername())
		if err != nil {
			continue
		}
		responses = append(responses, s.streamAPIResponse(stream))
	}
	writeStreamJSON(w, http.StatusOK, responses)
}

func (s *Server) handleStreamsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireStreamsAdmin(w, r) {
		return
	}
	if s.streamManager == nil {
		http.Error(w, "Stream manager unavailable", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	stream, err := s.streamManager.CreateStream(req.Username, "", s.adminUsername())
	if err != nil {
		writeStreamError(w, http.StatusBadRequest, err)
		return
	}
	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}

	// CreateStream is authoritative: it trims and canonicalizes the username
	// and generates the token, so neither value is reconstructed from the request.
	writeStreamJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"user":    s.streamAPIResponse(stream),
	})
}

func (s *Server) streamUsernameFromRequest(r *http.Request) (string, string, error) {
	path := strings.TrimPrefix(r.URL.Path, apiStreamsPrefix)
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", fmt.Errorf("username required")
	}
	username, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(username) == "" {
		return "", "", fmt.Errorf("invalid username")
	}
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.Join(parts[1:], "/")
	}
	return username, suffix, nil
}

func (s *Server) handleStreamByUsername(w http.ResponseWriter, r *http.Request) {
	if !s.requireStreamsAdmin(w, r) {
		return
	}
	if s.streamManager == nil {
		http.Error(w, "Stream manager unavailable", http.StatusServiceUnavailable)
		return
	}
	username, suffix, err := s.streamUsernameFromRequest(r)
	if err != nil {
		writeStreamError(w, http.StatusBadRequest, err)
		return
	}

	switch {
	case r.Method == http.MethodDelete && suffix == "":
		if err := s.streamManager.DeleteStream(username); err != nil {
			writeStreamError(w, http.StatusBadRequest, err)
			return
		}
		if s.strmServer != nil {
			s.strmServer.ClearSearchCaches()
		}
		writeStreamJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Stream %s deleted successfully", username),
		})
	case r.Method == http.MethodPost && suffix == "regenerate-token":
		token, err := s.streamManager.RegenerateToken(username)
		if err != nil {
			writeStreamError(w, http.StatusBadRequest, err)
			return
		}
		writeStreamJSON(w, http.StatusOK, map[string]interface{}{
			"success":      true,
			"token":        token,
			"manifest_url": s.manifestURL(token),
		})
	default:
		if suffix != "" {
			http.Error(w, "Not found", http.StatusNotFound)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (s *Server) handlePutStreamConfigs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.requireStreamsAdmin(w, r) {
		return
	}
	if s.streamManager == nil {
		http.Error(w, "Stream manager unavailable", http.StatusServiceUnavailable)
		return
	}

	var streamConfigs map[string]streamAssignmentsRequest
	if err := json.NewDecoder(r.Body).Decode(&streamConfigs); err != nil {
		writeStreamJSON(w, http.StatusBadRequest, map[string]interface{}{
			"status":  "error",
			"message": "Invalid stream config data",
		})
		return
	}

	var errors []string
	updated := false
	for username, assignments := range streamConfigs {
		if strings.EqualFold(strings.TrimSpace(username), strings.TrimSpace(s.adminUsername())) {
			continue
		}
		indexerSelections := append([]string(nil), assignments.IndexerSelections...)
		indexerOverrides := cloneIndexerOverrides(assignments.IndexerOverrides)
		if assignments.AutoAddIndexers != nil && *assignments.AutoAddIndexers && s.config != nil {
			indexerSelections = syncOrderedSelections(indexerSelections, enabledIndexerNames(s.config.Indexers))
			indexerOverrides = filterIndexerOverrides(indexerOverrides, indexerSelections)
		}
		streamConfig := &auth.Stream{
			FilterSortingMode:   assignments.FilterSortingMode,
			IndexerMode:         assignments.IndexerMode,
			CombineResults:      assignments.CombineResults,
			EnableFailover:      assignments.EnableFailover,
			ResultsMode:         assignments.ResultsMode,
			AutoAddIndexers:     assignments.AutoAddIndexers,
			IndexerOverrides:    indexerOverrides,
			IndexerSelections:   indexerSelections,
			MovieSearchQueries:  assignments.MovieSearchQueries,
			SeriesSearchQueries: assignments.SeriesSearchQueries,
		}
		if err := s.streamManager.UpdateStreamConfig(username, streamConfig); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to update stream config for %s: %v", username, err))
			continue
		}
		updated = true
	}
	if updated && s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	if len(errors) > 0 {
		writeStreamJSON(w, http.StatusBadRequest, map[string]interface{}{
			"status":  "error",
			"message": "Some stream configs failed to save",
			"errors":  errors,
		})
		return
	}
	writeStreamJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"message": "Stream configurations saved successfully. Search cache cleared.",
	})
}
