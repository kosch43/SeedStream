package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/logger"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Success            bool   `json:"success"`
	User               string `json:"user,omitempty"`
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	Error              string `json:"error,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if s.config == nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	adminUsername := s.config.GetAdminUsername()
	if !strings.EqualFold(strings.TrimSpace(req.Username), adminUsername) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid credentials",
		})
		return
	}

	if s.streamManager == nil || s.config.AdminSessionToken == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid credentials",
		})
		return
	}

	_, err := s.streamManager.Authenticate(req.Username, req.Password, adminUsername, s.config.AdminPasswordHash)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Invalid credentials",
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    s.config.AdminSessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.config.AddonBaseURL)), "https://"),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":              true,
		"user":                 adminUsername,
		"is_admin":             true,
		"must_change_password": s.config.AdminMustChangePassword,
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	version := "dev"
	if s.strmServer != nil {
		version = s.strmServer.Version()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"version": version})
}

func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	cookiePresent := false
	cookie, err := r.Cookie("auth_session")
	if err == nil && cookie != nil {
		cookiePresent = true
	}
	ok := s.config != nil && cookiePresent && auth.ValidAdminSessionToken(cookie.Value, s.config.AdminSessionToken)
	if ok {
		logger.Debug("Auth check authenticated", "via", "cookie")
	}

	logger.Debug("Auth check evaluated", "ok", ok, "cookie_present", cookiePresent)

	if ok {
		out := map[string]interface{}{
			"authenticated":        true,
			"username":             s.config.GetAdminUsername(),
			"is_admin":             true,
			"must_change_password": s.config.AdminMustChangePassword,
		}
		if s.strmServer != nil {
			out["version"] = s.strmServer.Version()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": false,
		})
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || (s.config != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.config.AddonBaseURL)), "https://")),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !auth.IsAdminContext(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Username        string `json:"username"`
		CurrentPassword string `json:"current_password"`
		Password        string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}
	adminUsername := s.config.GetAdminUsername()
	if req.Username != "" && req.Username != adminUsername {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid username"})
		return
	}
	if len(strings.TrimSpace(req.Password)) < 6 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Password must be at least 6 characters long"})
		return
	}
	if !s.config.AdminMustChangePassword && !auth.CheckPassword(req.CurrentPassword, s.config.AdminPasswordHash) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Current password is incorrect"})
		return
	}
	newHash := auth.HashPassword(req.Password)
	s.configSaveMu.Lock()
	defer s.configSaveMu.Unlock()
	s.mu.RLock()
	current := s.config
	next := *current
	s.mu.RUnlock()
	next.AdminPasswordHash = newHash
	next.AdminMustChangePassword = false
	if err := next.Save(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.config = &next
	s.mu.Unlock()
	if err := next.RemoveBootstrapAdminPassword(); err != nil {
		logger.Warn("Failed to remove bootstrap admin password", "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Password updated successfully",
	})
}
