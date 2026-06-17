package stremio

import (
	"net/http"
	"os"
	"strings"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

func (s *Server) handleTorrentPlay(w http.ResponseWriter, r *http.Request, sess *session.Session, _ *auth.Stream) {
	if s.torrentManager == nil || !s.torrentManager.Enabled() {
		logger.Warn("Torrent play requested but no torrent client configured", "session", sess.ID)
		http.Error(w, "No torrent client configured. Add a qBittorrent client in Settings → Torrent Clients.", http.StatusServiceUnavailable)
		return
	}

	season, episode := 0, 0
	if sess.ContentIDs != nil {
		season = sess.ContentIDs.Season
		episode = sess.ContentIDs.Episode
	}

	res, err := s.torrentManager.PrepareForPlayback(r.Context(), sess.Release, season, episode, 0, 0, nil)
	if err != nil {
		logger.Warn("Torrent prepare failed", "session", sess.ID, "title", sess.Release.Title, "err", err)
		http.Error(w, "Torrent still preparing: "+err.Error(), http.StatusGatewayTimeout)
		return
	}

	// Register the torrent with Cerberus so the watchdog can correlate
	// stalled hashes back to their content IDs for re-search.
	if s.cerberusClient != nil && sess.Release != nil && sess.ContentIDs != nil {
		infoHash := strings.TrimSpace(sess.Release.InfoHash)
		if infoHash == "" && sess.Release.Magnet != "" {
			infoHash = torrent.InfoHashFromMagnet(sess.Release.Magnet)
		}
		if infoHash != "" {
			magnet := sess.Release.Magnet
			if magnet == "" {
				magnet = sess.Release.Link
			}
			ids := cerberus.ContentIDs{
				ImdbID:  sess.ContentIDs.ImdbID,
				TmdbID:  sess.ContentIDs.TmdbID,
				TvdbID:  sess.ContentIDs.TvdbID,
				Season:  sess.ContentIDs.Season,
				Episode: sess.ContentIDs.Episode,
			}
			if err := s.cerberusClient.RegisterTorrent(infoHash, ids, magnet, sess.Release.Title); err != nil {
				logger.Warn("Cerberus: failed to register torrent", "hash", infoHash, "err", err)
			}
		}
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

	// NOTE: seeking forward past the downloaded portion of an in-progress torrent
	// will result in a 416 Range Not Satisfiable (file not that large yet) or serve
	// zeros/sparse data if the filesystem pre-allocated the full file. Full fix
	// requires a qBittorrent-aware ReadSeeker that waits for each requested range
	// to be downloaded before reading — a future improvement.
	sess.SetSelectedPlaybackFile(res.Name)
	logger.Info("Serving torrent stream", "session", sess.ID, "file", res.Name, "size", stat.Size())
	http.ServeContent(w, r, res.Name, stat.ModTime(), f)
}
