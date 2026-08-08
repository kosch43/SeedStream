package auth

import "testing"

// newTokenTestManager builds a StreamManager directly (bypassing the global
// singleton) with two member streams that carry tokens.
func newTokenTestManager() *StreamManager {
	return &StreamManager{
		streams: map[string]*Stream{
			"default": {Username: "default", Token: "stream-token-default"},
			"alice":   {Username: "alice", Token: "stream-token-alice"},
			"notoken": {Username: "notoken", Token: ""},
		},
	}
}

func TestAuthenticateTokenMapsLegacyAdminTokenToDefaultStream(t *testing.T) {
	dm := newTokenTestManager()
	stream, err := dm.AuthenticateToken("admin-token", "admin", "admin-token", "admin-session-token")
	if err != nil {
		t.Fatalf("legacy addon token should authenticate: %v", err)
	}
	if stream.Username != "default" {
		t.Fatalf("expected default stream, got %q", stream.Username)
	}
}

func TestAuthenticateTokenRejectsAdminSessionToken(t *testing.T) {
	dm := newTokenTestManager()
	if _, err := dm.AuthenticateToken("admin-session-token", "admin", "admin-token", "admin-session-token"); err == nil {
		t.Fatal("admin session token must not authenticate as an addon stream")
	}
}

// TestAuthenticateTokenAcceptsStreamToken is the regression test for the fix:
// a per-stream token (the token embedded in the addon URL
// {base}/{token}/manifest.json) must authenticate. Before the fix only the
// admin token was accepted, so every member stream — including the
// auto-created "default" stream — was rejected with "invalid token".
func TestAuthenticateTokenAcceptsStreamToken(t *testing.T) {
	dm := newTokenTestManager()

	for _, tc := range []struct{ token, wantUser string }{
		{"stream-token-default", "default"},
		{"stream-token-alice", "alice"},
	} {
		stream, err := dm.AuthenticateToken(tc.token, "admin", "admin-token")
		if err != nil {
			t.Fatalf("stream token %q should authenticate: %v", tc.token, err)
		}
		if stream.Username != tc.wantUser {
			t.Fatalf("token %q resolved to %q, want %q", tc.token, stream.Username, tc.wantUser)
		}
	}
}

// TestAuthenticateTokenRejectsUnknownAndEmpty guards the negative cases — in
// particular an empty token must not match the stream whose token is empty.
func TestAuthenticateTokenRejectsUnknownAndEmpty(t *testing.T) {
	dm := newTokenTestManager()

	for _, token := range []string{"", "not-a-real-token"} {
		if _, err := dm.AuthenticateToken(token, "admin", "admin-token"); err == nil {
			t.Fatalf("token %q must be rejected", token)
		}
	}
}

// TestAuthenticateTokenReturnsCopy ensures callers cannot mutate stored state
// through the returned stream.
func TestAuthenticateTokenReturnsCopy(t *testing.T) {
	dm := newTokenTestManager()
	stream, err := dm.AuthenticateToken("stream-token-alice", "admin", "admin-token")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	stream.Username = "mutated"
	if dm.streams["alice"].Username != "alice" {
		t.Fatalf("returned stream must be a copy; stored stream was mutated")
	}
}
