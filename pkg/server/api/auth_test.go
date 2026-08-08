package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
)

func TestHandleLoginSetsAdminSessionCookieWithoutReturningToken(t *testing.T) {
	cfg := &config.Config{
		AdminUsername:     "admin",
		AdminPasswordHash: auth.HashPassword("correct horse battery staple"),
		AdminSessionToken: "admin-session-token",
		AdminToken:        "legacy-addon-token",
	}
	dm := &auth.StreamManager{}
	s := &Server{config: cfg, streamManager: dm}
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"admin","password":"correct horse battery staple"}`))
	rr := httptest.NewRecorder()
	s.handleLogin(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, exists := body["token"]; exists {
		t.Fatal("login response must not contain a raw token")
	}
	cookie := rr.Result().Cookies()[0]
	if cookie.Name != "auth_session" || cookie.Value != cfg.AdminSessionToken {
		t.Fatalf("cookie = %#v, want admin session token", cookie)
	}
}

func TestHandleAuthCheckAcceptsCookieOnly(t *testing.T) {
	cfg := &config.Config{AdminUsername: "admin", AdminSessionToken: "admin-session-token", AdminToken: "legacy-addon-token"}
	s := &Server{config: cfg}
	for _, tc := range []struct {
		name   string
		setup  func(*http.Request)
		status int
	}{
		{name: "legacy addon bearer rejected", setup: func(r *http.Request) { r.Header.Set("Authorization", "Bearer legacy-addon-token") }, status: http.StatusUnauthorized},
		{name: "admin bearer rejected", setup: func(r *http.Request) { r.Header.Set("Authorization", "Bearer admin-session-token") }, status: http.StatusUnauthorized},
		{name: "admin cookie accepted", setup: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "auth_session", Value: "admin-session-token"}) }, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)
			tc.setup(req)
			rr := httptest.NewRecorder()
			s.handleAuthCheck(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d", rr.Code, tc.status)
			}
		})
	}
}
