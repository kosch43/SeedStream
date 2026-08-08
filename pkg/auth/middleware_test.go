package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthMiddlewareRequiresAdminSessionCredential(t *testing.T) {
	dm := &StreamManager{streams: map[string]*Stream{
		"default": {Username: "default", Token: "default-stream-token"},
		"alice":   {Username: "alice", Token: "member-stream-token"},
	}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdminContext(r) {
			t.Fatal("expected explicit admin principal")
		}
		stream, ok := StreamFromContext(r)
		if !ok || stream == nil || stream.Token != "default-stream-token" {
			t.Fatalf("expected default compatibility stream, got %#v", stream)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := AuthMiddleware(dm, func() string { return "admin" }, func() string { return "admin-session-token" })(next)

	for _, tc := range []struct {
		name   string
		setup  func(*http.Request)
		status int
	}{
		{name: "stream token bearer", setup: func(r *http.Request) { r.Header.Set("Authorization", "Bearer member-stream-token") }, status: http.StatusUnauthorized},
		{name: "legacy addon token bearer", setup: func(r *http.Request) { r.Header.Set("Authorization", "Bearer legacy-addon-token") }, status: http.StatusUnauthorized},
		{name: "query token", setup: func(r *http.Request) { r.URL.RawQuery = "token=admin-session-token" }, status: http.StatusUnauthorized},
		{name: "admin session bearer", setup: func(r *http.Request) { r.Header.Set("Authorization", "Bearer admin-session-token") }, status: http.StatusNoContent},
		{name: "admin session cookie", setup: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "auth_session", Value: "admin-session-token"}) }, status: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			tc.setup(req)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d", rr.Code, tc.status)
			}
		})
	}
}

func TestContextWithStreamIsNotAnAdminPrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req = req.WithContext(ContextWithStream(req.Context(), &Stream{Username: "admin"}))
	if IsAdminContext(req) {
		t.Fatal("stream username must not grant admin principal")
	}
}
