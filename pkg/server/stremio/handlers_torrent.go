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

	// Derive info-hash and content IDs early so we can run the Cerberus
	// block-check before the expensive PrepareForPlayback call.
	var infoHash string
	var cerberusIDs cerberus.ContentIDs
	if sess.Release != nil {
		infoHash = strings.TrimSpace(sess.Release.InfoHash)
		if infoHash == "" && sess.Release.Magnet != "" {
			infoHash = torrent.InfoHashFromMagnet(sess.Release.Magnet)
		}
	}
	if sess.ContentIDs != nil {
		cerberusIDs = cerberus.ContentIDs{
			ImdbID:  sess.ContentIDs.ImdbID,
			TmdbID:  sess.ContentIDs.TmdbID,
			TvdbID:  sess.ContentIDs.TvdbID,
			Season:  sess.ContentIDs.Season,
			Episode: sess.ContentIDs.Episode,
		}
	}

	// Reject torrents already on the Cerberus blocklist before we spend time
	// adding them to qBittorrent. Stremio will fall through to the next stream.
	if infoHash != "" && s.cerberusClient != nil && s.cerberusClient.IsBlocked(infoHash) {
		logger.Info("Cerberus: blocked torrent rejected at playback", "hash", infoHash, "session", sess.ID)
		http.Error(w, "Torrent is in the Cerberus health blocklist. Stremio will try the next result.", http.StatusServiceUnavailable)
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
	if s.cerberusClient != nil && infoHash != "" && sess.Release != nil && sess.ContentIDs != nil {
		magnet := sess.Release.Magnet
		if magnet == "" {
			magnet = sess.Release.Link
		}
		if err := s.cerberusClient.RegisterTorrent(infoHash, cerberusIDs, magnet, sess.Release.Title, sess.Release.Indexer); err != nil {
			logger.Warn("Cerberus: failed to register torrent", "hash", infoHash, "err", err)
		}
	}

	f, err := s.torrentManager.OpenForPlayback(res)
	if err != nil {
		logger.Error("Torrent file not readable", "session", sess.ID, "path", res.AbsPath, "err", err)
		http.Error(w, "Torrent file not readable by SeedStream. Check that the qBittorrent save path is mounted and readable.", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := os.Stat(res.AbsPath)
	if err != nil {
		http.Error(w, "stat failed", http.StatusInternalServerError)
		return
	}

	sess.SetSelectedPlaybackFile(res.Name)
	logger.Info("Serving torrent stream", "session", sess.ID, "file", res.Name, "size", stat.Size(), "progress", res.Progress)

	// Wrap the ResponseWriter so we can count bytes actually transferred.
	// A multi-megabyte successful read is strong evidence the torrent is healthy;
	// report it to Cerberus so a backend can build positive reputation signals.
	crw := &countingResponseWriter{ResponseWriter: w}
	http.ServeContent(crw, r, res.Name, stat.ModTime(), f)

	const minHealthyBytes = 512 * 1024
	if s.cerberusClient != nil && infoHash != "" && crw.written >= minHealthyBytes {
		s.cerberusClient.ReportHealthy(infoHash, cerberusIDs)
	}
}

// countingResponseWriter wraps http.ResponseWriter to track bytes written.
// It also forwards Flush calls so HTTP streaming is not affected.
type countingResponseWriter struct {
	http.ResponseWriter
	written int64
}

func (c *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.written += int64(n)
	return n, err
}

func (c *countingResponseWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
