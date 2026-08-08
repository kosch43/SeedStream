package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/paths"
)

type configPayload struct {
	config.Config
	EnvOverrides []string `json:"env_overrides,omitempty"`
}

func configForAdminAPI(cfg *config.Config) config.Config {
	if cfg == nil {
		return config.Config{}
	}
	out := *cfg
	out.AdminPasswordHash = ""
	out.AdminSessionToken = ""
	out.AdminToken = ""
	return out
}

func redactedConfigForViewer(cfg *config.Config) config.Config {
	return cfg.RedactForAPI()
}

func mergeConfigPatch(current *config.Config, body []byte) (*config.Config, error) {
	if current == nil {
		return nil, fmt.Errorf("configuration unavailable")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return nil, fmt.Errorf("config payload must be a JSON object")
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare config update: %w", err)
	}
	var next config.Config
	if err := json.Unmarshal(currentJSON, &next); err != nil {
		return nil, fmt.Errorf("failed to prepare config update: %w", err)
	}
	if err := json.Unmarshal(body, &next); err != nil {
		return nil, fmt.Errorf("invalid config data: %w", err)
	}
	next.LoadedPath = current.LoadedPath
	return &next, nil
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleGetConfig(w, r)
		return
	}
	if r.Method == http.MethodPut {
		s.handlePutConfig(w, r)
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.config == nil {
		http.Error(w, "Configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	var cfg config.Config
	if auth.IsAdminContext(r) {
		cfg = configForAdminAPI(s.config)
	} else {
		cfg = redactedConfigForViewer(s.config)
	}
	if cfg.AdminUsername == "" {
		cfg.AdminUsername = s.config.GetAdminUsername()
	}
	w.Header().Set("Content-Type", "application/json")
	if auth.IsAdminContext(r) {
		envKeys := config.GetEnvOverrideKeys()
		json.NewEncoder(w).Encode(configPayload{Config: cfg, EnvOverrides: envKeys})
	} else {
		json.NewEncoder(w).Encode(cfg)
	}
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !auth.IsAdminContext(r) {
		http.Error(w, "Only admin can save global configuration", http.StatusForbidden)
		return
	}
	s.configSaveMu.Lock()
	defer s.configSaveMu.Unlock()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeSaveStatus(w, "error", "Invalid config data", nil)
		return
	}

	s.mu.RLock()
	currentCfg := s.config
	currentLoadedPath := s.config.LoadedPath
	s.mu.RUnlock()

	newCfg, err := mergeConfigPatch(currentCfg, body)
	if err != nil {
		s.writeSaveStatus(w, "error", err.Error(), nil)
		return
	}

	plan := validationPlanFromPatch(body, currentCfg, newCfg)
	fieldErrors := s.validateConfigWithPlan(newCfg, plan)
	if len(fieldErrors) > 0 {
		s.writeSaveStatus(w, "error", "Validation failed", fieldErrors)
		return
	}

	config.CopyEnvOverridesFrom(currentCfg, newCfg)
	// Authentication fields are server-managed and never accepted from API
	// payloads, including redacted payloads.
	newCfg.AdminPasswordHash = currentCfg.AdminPasswordHash
	newCfg.AdminSessionToken = currentCfg.AdminSessionToken
	newCfg.AdminToken = currentCfg.AdminToken
	newCfg.AdminMustChangePassword = currentCfg.AdminMustChangePassword
	newCfg.Streams = cloneStreamEntries(currentCfg.Streams)
	applyStreamAutoSelections(newCfg)
	if newCfg.AdminUsername == "" {
		newCfg.AdminUsername = currentCfg.GetAdminUsername()
	}
	if currentLoadedPath == "" {
		currentLoadedPath = filepath.Join(paths.GetDataDir(), "config.json")
	}
	newCfg.LoadedPath = currentLoadedPath
	if err := newCfg.Save(); err != nil {
		s.writeSaveStatus(w, "error", "Failed to save config: "+err.Error(), nil)
		return
	}
	s.mu.Lock()
	s.config = newCfg
	s.mu.Unlock()
	if s.strmServer != nil {
		s.strmServer.ClearSearchCaches()
	}
	s.reloadConfigAsync(newCfg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Configuration saved and reloaded. Search cache cleared.",
	})
}

func (s *Server) writeSaveStatus(w http.ResponseWriter, status, message string, errors map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	if status == "error" {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  status,
		"message": message,
		"errors":  errors,
	})
}

func (s *Server) handleClearCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !auth.IsAdminContext(r) {
		http.Error(w, "Only admin can clear caches", http.StatusForbidden)
		return
	}
	if s.strmServer == nil {
		http.Error(w, "Streaming server unavailable", http.StatusServiceUnavailable)
		return
	}
	s.strmServer.ClearSearchCaches()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "success",
		"message": "Search cache cleared.",
	})
}
