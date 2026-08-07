package api

import "net/http"

// handleErrorVideoStatus reports which failure clip playback falls back to, and
// where to drop a replacement.
//
// The clip is the only channel there is for telling a viewer why nothing is
// playing: by the time playback fails, the player is committed to a video
// stream and will not render a message. Which clip that is, and how to change
// it, is therefore worth stating in the UI rather than leaving to be discovered
// in the source.
func (s *Server) handleErrorVideoStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	strm := s.strmServer
	s.mu.RUnlock()
	if strm == nil {
		writeJSON(w, map[string]any{"custom": false})
		return
	}
	writeJSON(w, strm.ErrorVideoStatus())
}
