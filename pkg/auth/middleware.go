package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const (
	userContextKey      contextKey = "user"
	principalContextKey contextKey = "principal"
)

// Principal identifies the authorization identity independently of the stream
// configuration used by handlers. Admin sessions are deliberately not stream
// credentials.
type Principal struct {
	Username string
	Admin    bool
}

func StreamFromContext(r *http.Request) (*Stream, bool) {
	if r == nil {
		return nil, false
	}
	stream, ok := r.Context().Value(userContextKey).(*Stream)
	return stream, ok
}

// ContextWithStream stores the authenticated stream in the context.
func ContextWithStream(ctx context.Context, stream *Stream) context.Context {
	return context.WithValue(ctx, userContextKey, stream)
}

// ContextWithAdmin stores an explicit admin principal and a compatibility
// stream for handlers that still consume StreamFromContext.
func ContextWithAdmin(ctx context.Context, username string, compatibilityStream *Stream) context.Context {
	principal := Principal{Username: username, Admin: true}
	ctx = context.WithValue(ctx, principalContextKey, principal)
	if compatibilityStream == nil {
		compatibilityStream = &Stream{Username: username}
	}
	return ContextWithStream(ctx, compatibilityStream)
}

// PrincipalFromContext returns the explicit authorization principal, if one
// was installed by authentication middleware.
func PrincipalFromContext(r *http.Request) (Principal, bool) {
	if r == nil {
		return Principal{}, false
	}
	principal, ok := r.Context().Value(principalContextKey).(Principal)
	return principal, ok
}

// IsAdminContext reports whether the request was authenticated as an admin.
// It intentionally does not infer admin access from a stream username.
func IsAdminContext(r *http.Request) bool {
	principal, ok := PrincipalFromContext(r)
	return ok && principal.Admin
}

// ValidAdminSessionToken compares a presented dashboard session token with the
// configured token without treating any addon stream token as an admin token.
func ValidAdminSessionToken(presented, configured string) bool {
	if presented == "" || configured == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(configured)) == 1
}

func adminCompatibilityStream(streamManager *StreamManager, adminUsername string) *Stream {
	if streamManager != nil {
		if stream, err := streamManager.GetStream("default", adminUsername); err == nil && stream != nil {
			// Keep the historical admin username visible to existing handlers while
			// using the default stream's canonical addon token for dashboard searches.
			stream.Username = adminUsername
			return stream
		}
	}
	return &Stream{Username: adminUsername}
}

// AuthMiddleware authenticates dashboard API requests. The third callback is
// the admin session token, not an addon stream token. The stream manager is
// used only to provide compatibility stream settings to existing handlers.
func AuthMiddleware(streamManager *StreamManager, getAdminUsername, getAdminSessionToken func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			adminUsername := ""
			if getAdminUsername != nil {
				adminUsername = getAdminUsername()
			}
			adminSessionToken := ""
			if getAdminSessionToken != nil {
				adminSessionToken = getAdminSessionToken()
			}

			cookie, err := r.Cookie("auth_session")
			if err == nil && cookie != nil && ValidAdminSessionToken(cookie.Value, adminSessionToken) {
				ctx := ContextWithAdmin(r.Context(), adminUsername, adminCompatibilityStream(streamManager, adminUsername))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if err == nil && cookie != nil {
				// Cookie present but invalid (e.g. after container restart with a new token).
				// Clear it so the browser doesn't keep sending a stale credential.
				http.SetCookie(w, &http.Cookie{
					Name:     "auth_session",
					Value:    "",
					Path:     "/",
					HttpOnly: true,
					MaxAge:   -1,
				})
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && ValidAdminSessionToken(strings.TrimSpace(parts[1]), adminSessionToken) {
					ctx := ContextWithAdmin(r.Context(), adminUsername, adminCompatibilityStream(streamManager, adminUsername))
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Unauthorized",
			})
		})
	}
}

// StreamAuthMiddleware is retained as a compatibility name for callers that
// install the dashboard middleware. Its token callback is an admin session
// token; addon stream authentication belongs to the Stremio resolver.
func StreamAuthMiddleware(streamManager *StreamManager, getAdminUsername, getAdminSessionToken func() string) func(http.Handler) http.Handler {
	return AuthMiddleware(streamManager, getAdminUsername, getAdminSessionToken)
}
