package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
)

// handleMemberSetup lets a member update their own Prowlarr + qBittorrent config.
func (s *Server) handleMemberSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stream, _ := auth.StreamFromContext(r)
	if stream == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if stream.Username == s.config.GetAdminUsername() {
		http.Error(w, "Admin should use the main settings", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodGet {
		// Return current member setup (redact qBit password)
		tc := stream.TorrentClient
		var tcOut interface{}
		if tc != nil {
			tcOut = map[string]interface{}{
				"url":       tc.URL,
				"username":  tc.Username,
				"category":  tc.CategoryOrDefault(),
				"save_path": tc.SavePath,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"username":       stream.Username,
			"token":          stream.Token,
			"prowlarr_url":   stream.ProwlarrURL,
			"torrent_client": tcOut,
		})
		return
	}

	// PUT: update member's own Prowlarr + qBit config
	var req struct {
		ProwlarrURL    string                      `json:"prowlarr_url"`
		ProwlarrAPIKey string                      `json:"prowlarr_api_key"`
		TorrentClient  *config.TorrentClientConfig `json:"torrent_client"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Build updated stream — preserve all existing settings, only change connections
	updated := &auth.Stream{
		FilterSortingMode:   stream.FilterSortingMode,
		IndexerMode:         stream.IndexerMode,
		UseAvailNZB:         stream.UseAvailNZB,
		CombineResults:      stream.CombineResults,
		EnableFailover:      stream.EnableFailover,
		ResultsMode:         stream.ResultsMode,
		AutoAddProviders:    stream.AutoAddProviders,
		AutoAddIndexers:     stream.AutoAddIndexers,
		IndexerOverrides:    stream.IndexerOverrides,
		ProviderSelections:  stream.ProviderSelections,
		IndexerSelections:   stream.IndexerSelections,
		MovieSearchQueries:  stream.MovieSearchQueries,
		SeriesSearchQueries: stream.SeriesSearchQueries,
		TorrentClient:       req.TorrentClient,
		ProwlarrURL:         strings.TrimSpace(req.ProwlarrURL),
		ProwlarrAPIKey:      strings.TrimSpace(req.ProwlarrAPIKey),
		PasswordHash:        stream.PasswordHash,
		MustChangePassword:  stream.MustChangePassword,
	}
	if err := s.streamManager.UpdateStreamConfig(stream.Username, updated); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}
