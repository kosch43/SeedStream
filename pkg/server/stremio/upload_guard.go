package stremio

import (
	"fmt"
	"strconv"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// applyUploadGuard decides how to serve a title while the monthly upload cap is
// in force. It returns the head-buffer size PrepareForPlayback should use and,
// when the title is too heavy to stream within the post-cap throttle, a non-nil
// error whose message is the user-facing disclaimer (the title is held until the
// allowance resets). Returns (0, nil) when the guard is off or not yet throttled.
//
// The decisive control is the bitrate gate: SeedStream cannot lift the provider's
// throttle, so a title whose average bitrate exceeds the throttle would buffer no
// matter how much is pre-buffered, and is held. A title that fits is served with
// an enlarged head buffer so playback starts with a bigger cushion.
func (s *Server) applyUploadGuard(sess *session.Session) (int64, error) {
	if s.uploadMeter == nil || !s.config.UploadGuardEnabled() {
		return 0, nil
	}
	s.uploadMeter.SetLimits(s.config.MonthlyUploadCapBytes(), s.config.EffectiveUploadCapResetDay())
	if !s.uploadMeter.Throttled() {
		return 0, nil
	}

	throttleMbps := s.config.EffectivePostCapUploadMbps()
	isSeries := sess.ContentIDs != nil && (sess.ContentIDs.Season > 0 || sess.ContentIDs.Episode > 0)
	runtimeSec := s.resolveRuntimeSeconds(sess)
	if runtimeSec <= 0 {
		runtimeSec = nominalRuntimeSeconds(isSeries)
	}
	var sizeBytes int64
	if sess.Release != nil {
		sizeBytes = sess.Release.Size
	}

	if sizeBytes > 0 && runtimeSec > 0 {
		bitrateMbps := float64(sizeBytes) * 8 / float64(runtimeSec) / 1e6
		// Require headroom: seeding for ratio and protocol overhead also draw on
		// the throttled pipe, so a title that only "just fits" would still stall.
		if bitrateMbps > throttleMbps*0.85 {
			logger.Info("Upload guard: holding heavy title until allowance resets",
				"session", sess.ID, "bitrate_mbps", bitrateMbps, "throttle_mbps", throttleMbps)
			return 0, fmt.Errorf("monthly upload allowance reached — this release needs about %.0f Mbps but the seedbox is throttled to %.0f Mbps until the allowance resets on day %d of the month; pick a smaller or lower-quality release, or try again after the reset",
				bitrateMbps, throttleMbps, s.config.EffectiveUploadCapResetDay())
		}
	}
	return throttledBufferBytes(sizeBytes, runtimeSec), nil
}

// resolveRuntimeSeconds returns the title's runtime in seconds from TMDB, or 0
// when it cannot be determined. Positive results are cached per content id;
// failures are not cached, so a transient lookup error is retried next time.
func (s *Server) resolveRuntimeSeconds(sess *session.Session) int64 {
	if s.tmdbClient == nil || sess == nil || sess.ContentIDs == nil {
		return 0
	}
	ids := sess.ContentIDs
	isSeries := ids.Season > 0 || ids.Episode > 0
	key := runtimeCacheKey(ids.ImdbID, ids.TmdbID, isSeries)
	if key == "" {
		return 0
	}
	if v, ok := s.runtimeCache.Load(key); ok {
		return v.(int64)
	}
	secs := s.fetchRuntimeSeconds(ids.ImdbID, ids.TmdbID, isSeries)
	if secs > 0 {
		s.runtimeCache.Store(key, secs)
	}
	return secs
}

// fetchRuntimeSeconds looks the runtime up on TMDB. For a series it uses the
// show's typical episode length (episode_run_time), which is a good proxy for
// any single episode's bitrate.
func (s *Server) fetchRuntimeSeconds(imdbID, tmdbID string, isSeries bool) int64 {
	if isSeries {
		id := tmdbID
		if id == "" && imdbID != "" {
			if find, err := s.tmdbClient.Find(imdbID, "imdb_id"); err == nil && len(find.TVResults) > 0 {
				id = strconv.Itoa(find.TVResults[0].ID)
			}
		}
		n, err := strconv.Atoi(id)
		if err != nil {
			return 0
		}
		d, err := s.tmdbClient.GetTVDetails(n)
		if err != nil || d == nil || len(d.EpisodeRunTime) == 0 || d.EpisodeRunTime[0] <= 0 {
			return 0
		}
		return int64(d.EpisodeRunTime[0]) * 60
	}
	id := tmdbID
	if id == "" && imdbID != "" {
		if find, err := s.tmdbClient.Find(imdbID, "imdb_id"); err == nil && len(find.MovieResults) > 0 {
			id = strconv.Itoa(find.MovieResults[0].ID)
		}
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0
	}
	d, err := s.tmdbClient.GetMovieDetails(n)
	if err != nil || d == nil || d.Runtime <= 0 {
		return 0
	}
	return int64(d.Runtime) * 60
}

func runtimeCacheKey(imdbID, tmdbID string, isSeries bool) string {
	prefix := "movie"
	if isSeries {
		prefix = "tv"
	}
	if tmdbID != "" {
		return prefix + ":tmdb:" + tmdbID
	}
	if imdbID != "" {
		return prefix + ":imdb:" + imdbID
	}
	return ""
}

// nominalRuntimeSeconds is the fallback runtime when metadata lookup fails, so
// the bitrate gate still has a denominator: ~2h for a movie, ~45m for an episode.
func nominalRuntimeSeconds(isSeries bool) int64 {
	if isSeries {
		return 45 * 60
	}
	return 120 * 60
}

// throttledBufferBytes enlarges the on-disk head buffer while throttled so
// playback starts with a bigger cushion. Download is unmetered, so a larger
// pre-buffer fills quickly; it is capped so startup never waits absurdly long.
func throttledBufferBytes(sizeBytes, runtimeSec int64) int64 {
	base := torrent.DefaultBufferBytes
	if sizeBytes <= 0 || runtimeSec <= 0 {
		return base * 4
	}
	const prebufferSeconds = 60
	want := sizeBytes * prebufferSeconds / runtimeSec

	maxCap := sizeBytes / 5
	const absoluteCap = 512 * 1024 * 1024
	if maxCap > absoluteCap {
		maxCap = absoluteCap
	}
	if want > maxCap {
		want = maxCap
	}
	if want < base {
		want = base
	}
	return want
}
