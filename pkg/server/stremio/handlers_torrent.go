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
	"time"

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
// prepareTimeout bounds how long this attempt may wait for the torrent's head
// to buffer. It is the time left in the overall /play budget, so a chain of
// fallback attempts cannot collectively overrun an upstream proxy's limit.
func (s *Server) serveTorrent(w http.ResponseWriter, r *http.Request, sess *session.Session, _ *auth.Stream, prepareTimeout time.Duration) error {
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

	// Refuse to serve a release that is not the content that was asked for.
	// Search-time title validation is skipped whenever TMDB could not supply an
	// expected title, which lets unrelated releases reach a slot; without this
	// check the player receives a stream that does not match its request and
	// simply fails to start. Returning an error here hands the request to the
	// failover path, which tries the next slot instead — and does so before the
	// expensive prepare/buffer wait rather than after it.
	if err := releaseMatchesRequest(sess.Release, season, episode); err != nil {
		logger.Warn("Rejecting mismatched release before playback",
			"session", sess.ID, "requested_season", season, "requested_episode", episode,
			"release", sess.Release.Title, "reason", err)
		return fmt.Errorf("release does not match the requested content: %w", err)
	}

	// Refuse a swarm too thin to keep ahead of playback. Doing this before the
	// torrent is handed to qBittorrent converts a guaranteed stall into an
	// immediate failover to a better-seeded release.
	if err := releaseHasEnoughSeeders(sess.Release, s.config.EffectiveMinSeeders()); err != nil {
		logger.Info("Rejecting under-seeded release before playback",
			"session", sess.ID, "release", sess.Release.Title,
			"min_seeders", s.config.EffectiveMinSeeders(), "reason", err)
		return fmt.Errorf("release does not have enough seeders: %w", err)
	}

	// Monthly upload guard: once the seedbox's allowance is spent the provider
	// throttles upload, so hold titles too heavy to stream within that throttle
	// (returning the disclaimer to the viewer) and give the rest a bigger head
	// buffer. A no-op when the guard is off or the allowance is not yet reached.
	bufferBytes, holdErr := s.applyUploadGuard(sess)
	if holdErr != nil {
		logger.Info("Torrent play held by upload guard", "session", sess.ID, "title", sess.Release.Title, "reason", holdErr)
		return holdErr
	}

	// Size the head buffer by playback time rather than bytes when the upload
	// guard has not already chosen one. A fixed byte count is far too small a
	// cushion for a high-bitrate remux, which is why such releases start and then
	// immediately rebuffer.
	if bufferBytes <= 0 {
		bufferBytes = s.playbackBufferBytes(sess, s.config.EffectiveTorrentBufferBytes())
	}

	// Cancel preparation when either the request ends or the session is
	// closed (e.g. user closed it from the dashboard).
	prepCtx, prepCancel := mergeSessionContext(r, sess)
	defer prepCancel()

	// Replay from a torrent the seedbox already downloaded for this exact
	// content when the selected release is not itself already present, so a
	// second viewing starts immediately instead of downloading a fresh copy.
	playRelease := s.resolvePlayRelease(prepCtx, sess.Release, infoHash, cerberusIDs, season, episode)
	playHash := infoHash
	if playRelease != sess.Release {
		playHash = playRelease.InfoHash
	}

	res, err := s.torrentManager.PrepareForPlayback(prepCtx, playRelease, season, episode, bufferBytes, prepareTimeout, nil)
	if err != nil {
		logger.Warn("Torrent prepare failed", "session", sess.ID, "title", playRelease.Title, "err", err)
		return fmt.Errorf("torrent still preparing: %w", err)
	}

	// Register the torrent with Cerberus so the watchdog can correlate stalled
	// hashes back to their content IDs, and so a later viewing of this title can
	// find the torrent again instead of downloading a fresh copy. Records the
	// torrent actually played, which may be a remembered one rather than the
	// release the viewer picked.
	// Prefer the hash qBittorrent confirmed for the torrent actually being
	// played. The release's own hash is often absent — Torznab results that only
	// carry an .torrent link have none — and gating registration on it left the
	// registry empty, which silently disabled both the watchdog and replay of
	// already-downloaded torrents.
	registerHash := res.Hash
	if registerHash == "" {
		registerHash = playHash
	}
	if s.cerberusClient != nil && registerHash != "" && sess.ContentIDs != nil {
		magnet := playRelease.Magnet
		if magnet == "" {
			magnet = playRelease.Link
		}
		if err := s.cerberusClient.RegisterTorrent(registerHash, cerberusIDs, magnet, playRelease.Title, playRelease.Indexer); err != nil {
			logger.Warn("Cerberus: failed to register torrent", "hash", registerHash, "err", err)
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
	// Fold this stream's egress into the monthly upload guard so streaming, not
	// just BitTorrent seeding, counts toward the allowance.
	if s.uploadMeter != nil {
		s.uploadMeter.AddStreamBytes(crw.written)
	}
	const minHealthyBytes = 512 * 1024
	if crw.written >= minHealthyBytes {
		s.markSessionServedSuccessfully(sess.ID, sess)
		if s.cerberusClient != nil && registerHash != "" {
			s.cerberusClient.ReportHealthy(registerHash, cerberusIDs)
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
