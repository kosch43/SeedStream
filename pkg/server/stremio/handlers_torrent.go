package stremio

import (
	"net/http"
	"os"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/session"
)

// handleTorrentPlay serves a torrent release. The torrent is handed to a seedbox
// qBittorrent (which downloads sequentially and keeps seeding), and once the
// head of the chosen file has buffered we serve it from disk with HTTP range
// support via http.ServeContent.
func (s *Server) handleTorrentPlay(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if s.torrentManager == nil || !s.torrentManager.Enabled() {
		logger.Warn("Torrent play requested but no torrent client configured", "session", sess.ID)
		http.Error(w, "No torrent client configured. Add a qBittorrent client in Settings.", http.StatusServiceUnavailable)
		return
	}

	season, episode := 0, 0
	if sess.ContentIDs != nil {
		season = sess.ContentIDs.Season
		episode = sess.ContentIDs.Episode
	}

	res, err := s.torrentManager.PrepareForPlayback(r.Context(), sess.Release, season, episode, 0, 0)
	if err != nil {
		logger.Warn("Torrent prepare failed", "session", sess.ID, "title", sess.Release.Title, "err", err)
		// 504 signals the client to retry shortly; by then more has buffered.
		http.Error(w, "Torrent still preparing: "+err.Error(), http.StatusGatewayTimeout)
		return
	}

	f, err := os.Open(res.AbsPath)
	if err != nil {
		logger.Error("Torrent file not readable", "session", sess.ID, "path", res.AbsPath, "err", err)
		http.Error(w, "Torrent file not readable by SeedStream. Check that the qBittorrent save path is mounted and readable.", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}

	sess.SetSelectedPlaybackFile(res.Name)
	logger.Info("Serving torrent stream", "session", sess.ID, "file", res.Name, "size", stat.Size())
	http.ServeContent(w, r, res.Name, stat.ModTime(), f)
}
