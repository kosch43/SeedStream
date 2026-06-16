package stremio

import (
	"net/http"
	"os"
	"strings"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// handleTorrentPlay serves a torrent release. The torrent is handed to a seedbox
// qBittorrent (which downloads sequentially and keeps seeding), and once the
// head of the chosen file has buffered we serve it from disk with HTTP range
// support via http.ServeContent.
//
// streamConfig carries the per-stream qBittorrent override; if nil or has no
// URL set, the global torrent manager's first client is used instead.
func (s *Server) handleTorrentPlay(w http.ResponseWriter, r *http.Request, sess *session.Session, streamConfig *auth.Stream) {
	// Determine which client to use: per-stream override takes priority.
	var clientOverride *config.TorrentClientConfig
	if streamConfig != nil && streamConfig.TorrentClient != nil && strings.TrimSpace(streamConfig.TorrentClient.URL) != "" {
		clientOverride = streamConfig.TorrentClient
	}

	// If no per-stream client is set, fall back to the global list.
	if clientOverride == nil && (s.torrentManager == nil || !s.torrentManager.Enabled()) {
		logger.Warn("Torrent play requested but no torrent client configured", "session", sess.ID)
		http.Error(w, "No torrent client configured. Add a qBittorrent client in Settings → Torrent Clients, or configure one on this stream.", http.StatusServiceUnavailable)
		return
	}

	season, episode := 0, 0
	if sess.ContentIDs != nil {
		season = sess.ContentIDs.Season
		episode = sess.ContentIDs.Episode
	}

	res, err := s.torrentManager.PrepareForPlayback(r.Context(), sess.Release, season, episode, 0, 0, clientOverride)
	if err != nil {
		logger.Warn("Torrent prepare failed", "session", sess.ID, "title", sess.Release.Title, "err", err)
		http.Error(w, "Torrent still preparing: "+err.Error(), http.StatusGatewayTimeout)
		return
	}

	// Register the torrent with Cerberus so the watchdog can correlate
	// stalled hashes back to their content IDs for re-search.
	if s.cerberusClient != nil && sess.Release != nil && sess.ContentIDs != nil {
		infoHash := strings.TrimSpace(sess.Release.InfoHash)
		// Fall back to extracting the hash from the magnet URI when the release
		// does not carry an explicit InfoHash field (common with Torznab results).
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
