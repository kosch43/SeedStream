package api

import (
	"context"
	"net/http"
	"time"
)

// handleCerberusStatus exposes what the torrent watchdog knows: each tracked
// torrent's standing against its tracker's hit-and-run rules, and the health
// blocklist. Read-only.
func (s *Server) handleCerberusStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	strm := s.strmServer
	s.mu.RUnlock()
	if strm == nil {
		writeJSON(w, map[string]any{"enabled": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, strm.CerberusStatus(ctx))
}
