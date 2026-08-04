package stremio

import (
	"context"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"seedstream/pkg/auth"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// serveTorrent prepares the session's torrent release in qBittorrent and
// serves the video file with HTTP range support. No response bytes are
// written until preparation succeeds, so the caller can fail over to the
// next slot when an error is returned.
func (s *Server) serveTorrent(w http.ResponseWriter, r *http.Request, sess *session.Session, _ *auth.Stream) error {
	if s.torrentManager == nil || !s.torrentManager.Enabled() {
		logger.Warn("Torrent play requested but no torrent client configured", "session", sess.ID)
		return fmt.Errorf("no torrent client configured — add a qBittorrent client in Settings → Torrent Clients")
	}
	if sess.Release == nil || !sess.Release.IsTorrent() {
		return fmt.Errorf("session %s has no torrent release", sess.ID)
	}

	// Derive info-hash and content IDs early so we can run the Cerberus
	// block-check before the expensive PrepareForPlayback call.
	infoHash := strings.TrimSpace(sess.Release.InfoHash)
	if infoHash == "" && sess.Release.Magnet != "" {
		infoHash = torrent.InfoHashFromMagnet(sess.Release.Magnet)
	}
	var cerberusIDs cerberus.ContentIDs
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
	// adding them to qBittorrent; the caller falls over to the next slot.
	if infoHash != "" && s.cerberusClient != nil && s.cerberusClient.IsBlocked(infoHash) {
		logger.Info("Cerberus: blocked torrent rejected at playback", "hash", infoHash, "session", sess.ID)
		return fmt.Errorf("torrent is in the Cerberus health blocklist")
	}

	season, episode := 0, 0
	if sess.ContentIDs != nil {
		season = sess.ContentIDs.Season
		episode = sess.ContentIDs.Episode
	}

	// Cancel preparation when either the request ends or the session is
	// closed (e.g. user closed it from the dashboard).
	prepCtx, prepCancel := mergeSessionContext(r, sess)
	defer prepCancel()

	res, err := s.torrentManager.PrepareForPlayback(prepCtx, sess.Release, season, episode, 0, 0, nil)
	if err != nil {
		logger.Warn("Torrent prepare failed", "session", sess.ID, "title", sess.Release.Title, "err", err)
		return fmt.Errorf("torrent still preparing: %w", err)
	}

	// Register the torrent with Cerberus so the watchdog can correlate
	// stalled hashes back to their content IDs for re-search.
	if s.cerberusClient != nil && infoHash != "" && sess.ContentIDs != nil {
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
		return fmt.Errorf("torrent file not readable by SeedStream (check that the qBittorrent save path is mounted and readable): %w", err)
	}
	defer f.Close()

	stat, err := os.Stat(res.AbsPath)
	if err != nil {
		return fmt.Errorf("stat torrent file: %w", err)
	}

	// Preparation succeeded — from here on we serve and never return an error,
	// since response bytes may already be on the wire.
	sess.SetSelectedPlaybackFile(res.Name)
	s.sessionManager.MarkPlaybackValidated(sess.ID)

	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	s.sessionManager.StartPlayback(sess.ID, clientIP)
	var endOnce sync.Once
	defer endOnce.Do(func() { s.sessionManager.EndPlayback(sess.ID, clientIP) })

	logger.Info("Serving torrent stream", "session", sess.ID, "file", res.Name, "size", stat.Size(), "progress", res.Progress)

	// Set an explicit Content-Type from the file extension before
	// ServeContent runs. On minimal containers Go's mime table does not
	// know ".mkv", so ServeContent falls back to sniffing the first bytes
	// and mislabels Matroska (EBML) files as "video/webm" — which many
	// players refuse for H.265/REMUX content. Resolve the extension here
	// with a fallback map for common video containers, else let
	// ServeContent detect.
	if ctype := contentTypeForFile(res.Name); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}

	// Count bytes so that a successful read is strong evidence the torrent
	// is healthy: report it to Cerberus and mark the slot committed.
	crw := &countingResponseWriter{ResponseWriter: w}
	http.ServeContent(crw, r, res.Name, stat.ModTime(), f)

	sess.AddBytesRead(crw.written)
	const minHealthyBytes = 512 * 1024
	if crw.written >= minHealthyBytes {
		s.markSessionServedSuccessfully(sess.ID, sess)
		if s.cerberusClient != nil && infoHash != "" {
			s.cerberusClient.ReportHealthy(infoHash, cerberusIDs)
		}
	}
	return nil
}

// mergeSessionContext returns a context that is canceled when either the HTTP
// request ends or the session is closed from the dashboard.
func mergeSessionContext(r *http.Request, sess *session.Session) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	go func(done <-chan struct{}, sessDone <-chan struct{}) {
		select {
		case <-done:
		case <-sessDone:
			logger.Debug("playback aborted: session closed", "session", sess.ID)
			cancel()
		}
	}(ctx.Done(), sess.Done())
	return ctx, cancel
}

// contentTypeForFile returns the media type for a video file, preferring an
// explicit map so that Matroska files are never mislabeled as webm, then
// falling back to Go's mime table by extension.
func contentTypeForFile(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mkv":
		return "video/x-matroska"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ts":
		return "video/mp2t"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	case ".flv":
		return "video/x-flv"
	case ".wmv":
		return "video/x-ms-wmv"
	}
	if ctype := mime.TypeByExtension(ext); ctype != "" {
		return ctype
	}
	return ""
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
