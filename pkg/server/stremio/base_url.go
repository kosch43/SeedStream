package stremio

import (
	"fmt"
	"net/http"
	"strings"

	"seedstream/pkg/core/logger"
)

// resolveBaseURL returns the absolute URL SeedStream is reachable at, which is
// what every stream URL handed to a player is built from.
//
// An unset addon_base_url used to mean an empty string here, and the play URLs
// came out relative — "/<token>/play/…". Stremio cannot fetch those: it has no
// origin to resolve them against, so playback simply never starts and the
// download client is never even contacted. Nothing in the logs says why.
//
// So an empty setting is recovered from rather than propagated:
//
//  1. the configured addon_base_url, when set — the operator's own answer,
//     and the only one that can be right when SeedStream sits behind a proxy
//     that rewrites paths;
//  2. the host this very request arrived on, honouring the standard
//     reverse-proxy headers — right in almost every deployment, and derived
//     from evidence rather than guessed;
//  3. the local listen address, for the paths that build URLs without a
//     request in hand.
func (s *Server) resolveBaseURL(r *http.Request) string {
	s.mu.RLock()
	configured := strings.TrimSpace(s.baseURL)
	port := s.listenPort
	s.mu.RUnlock()

	if configured != "" {
		return strings.TrimSuffix(configured, "/")
	}

	derived := baseURLFromRequest(r)
	if derived == "" && port > 0 {
		derived = fmt.Sprintf("http://localhost:%d", port)
	}
	s.warnedNoBaseURL.Do(func() {
		logger.Warn("addon_base_url is not set — stream URLs are being built from the incoming request instead. Set it in Settings → General to the URL players reach SeedStream at.",
			"derived", derived)
	})
	return strings.TrimSuffix(derived, "/")
}

// baseURLFromRequest reconstructs the absolute origin a request was made to.
// Behind a reverse proxy the connection SeedStream sees is plain HTTP to an
// internal name, so the forwarded headers are preferred where present — they
// carry what the player actually asked for.
func baseURLFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	scheme := firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + host
}

// firstForwardedValue takes the first entry of a comma-separated proxy header.
// A request through two proxies arrives with "a.example, b.internal"; the
// first is the one the client used.
func firstForwardedValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
