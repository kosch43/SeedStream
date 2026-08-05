package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// loginServer builds a mock qBittorrent whose /auth/login responds with the
// given status and cookie name, and whose /app/version succeeds once a cookie
// has been presented.
func loginServer(t *testing.T, status int, cookieName, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if cookieName != "" {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "sessionvalue"})
		}
		w.WriteHeader(status)
		if body != "" {
			fmt.Fprint(w, body)
		}
	})
	mux.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v5.0.0")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestLoginQBittorrent5 is the regression test for the qBittorrent 5.x login
// fix: v5 answers the login with 204 No Content and names its session cookie
// QBT_SID_<port> rather than SID. Before the fix SeedStream rejected both, so
// it could not talk to a qBittorrent 5 seedbox at all.
func TestLoginQBittorrent5(t *testing.T) {
	srv := loginServer(t, http.StatusNoContent, "QBT_SID_8080", "")
	c := New(Options{BaseURL: srv.URL})

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("qBittorrent 5 login should succeed: %v", err)
	}
	if c.cookie != "QBT_SID_8080=sessionvalue" {
		t.Fatalf("unexpected stored cookie %q", c.cookie)
	}
}

// TestLoginQBittorrent4 keeps 4.x compatibility: 200 OK with a SID cookie.
func TestLoginQBittorrent4(t *testing.T) {
	srv := loginServer(t, http.StatusOK, "SID", "Ok.")
	c := New(Options{BaseURL: srv.URL})

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("qBittorrent 4 login should still succeed: %v", err)
	}
	if c.cookie != "SID=sessionvalue" {
		t.Fatalf("unexpected stored cookie %q", c.cookie)
	}
}

// TestLoginRejectsBadCredentials: qBittorrent answers 200 with the body
// "Fails." on wrong credentials, which must not be treated as success.
func TestLoginRejectsBadCredentials(t *testing.T) {
	srv := loginServer(t, http.StatusOK, "", "Fails.")
	c := New(Options{BaseURL: srv.URL})

	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("bad credentials must fail login")
	}
}

// TestLoginRejectsMissingCookie: a success status with no session cookie is
// still a failure — there is nothing to authenticate later calls with.
func TestLoginRejectsMissingCookie(t *testing.T) {
	srv := loginServer(t, http.StatusNoContent, "", "")
	c := New(Options{BaseURL: srv.URL})

	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("login without a session cookie must fail")
	}
}

// TestLoginRejectsErrorStatus covers a genuine HTTP failure (e.g. 403 from the
// WebUI host whitelist).
func TestLoginRejectsErrorStatus(t *testing.T) {
	srv := loginServer(t, http.StatusForbidden, "", "")
	c := New(Options{BaseURL: srv.URL})

	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("HTTP 403 must fail login")
	}
}
