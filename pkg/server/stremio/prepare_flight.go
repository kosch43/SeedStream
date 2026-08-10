package stremio

import (
	"context"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/release"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// prepareFlight is one in-progress (or completed) PrepareForPlayback for a
// session's torrent, shared by every request that wants it.
//
// A player does not open one connection per title. Stremio issues a probe and
// the real range request within milliseconds of each other, reconnects
// mid-playback, and re-requests ranges after a seek — and every one of those
// used to enter serveTorrent and start its own prepare loop for the same
// torrent. Two loops mean two of everything the loop does: two poll cycles,
// two re-anchor timers (reAnchorInterval paces each loop separately, so the
// download client gets twice the steering RPCs it was designed to receive),
// and two independent head requirements racing each other. Measured in the
// field: "opening head buffer" logged twice, five milliseconds apart, for a
// single viewing.
//
// Joining is not just deduplication. The second request arrives while the
// first is still buffering, so it wants exactly the answer the first is
// already computing; waiting for it is both cheaper and faster than starting
// over.
type prepareFlight struct {
	done   chan struct{}
	res    *torrent.PrepareResult
	err    error
	cancel context.CancelFunc

	// waiters counts callers that are waiting for the result or still using
	// it. The prepare's playback lease is released when this reaches zero —
	// releasing on the first caller to finish would demote the file's priority
	// while another request is still streaming from it.
	waiters int
}

// prepareGroup holds the flights currently in progress, keyed by session and
// torrent.
type prepareGroup struct {
	mu       sync.Mutex
	inflight map[string]*prepareFlight
}

// prepareKey identifies a torrent within a session. The hash is the natural
// key, but Torznab results that carry only a .torrent link have none until the
// client resolves one, so the release's own identity stands in — two requests
// for the same release in the same session are the same work either way.
func prepareKey(sess *session.Session, rel *release.Release, hash string) string {
	id := ""
	if sess != nil {
		id = sess.ID
	}
	target := strings.ToLower(strings.TrimSpace(hash))
	if target == "" && rel != nil {
		target = strings.TrimSpace(rel.Magnet)
	}
	if target == "" && rel != nil {
		target = strings.TrimSpace(rel.Link)
	}
	if target == "" && rel != nil {
		target = strings.TrimSpace(rel.Title)
	}
	return id + "\x00" + target
}

// preparePlayback runs PrepareForPlayback for this release, or joins the one
// already running for it, and returns the result plus a release function the
// caller must call when it has finished streaming.
//
// ctx bounds this caller's willingness to wait, not the prepare itself. The
// prepare runs on a context of its own that outlives any single request: a
// player that opens a probe connection and drops it must not cancel the
// buffering that the real request is waiting on. That context is cancelled
// when the session closes, or when the last caller walks away.
func (s *Server) preparePlayback(ctx context.Context, sess *session.Session, rel *release.Release,
	hash string, season, episode int, bufferBytes int64, timeout time.Duration) (*torrent.PrepareResult, func(), error) {

	key := prepareKey(sess, rel, hash)
	g := &s.prepares

	g.mu.Lock()
	if g.inflight == nil {
		g.inflight = make(map[string]*prepareFlight)
	}
	fl, joined := g.inflight[key]
	if !joined {
		flightCtx, cancel := context.WithCancel(context.Background())
		fl = &prepareFlight{done: make(chan struct{}), cancel: cancel}
		g.inflight[key] = fl
		profile := s.playbackProfile(sess)
		go func() {
			if sess != nil {
				// The session closing means the viewer is gone; nothing is
				// waiting for this buffer any more.
				go func() {
					select {
					case <-sess.Done():
						cancel()
					case <-fl.done:
					}
				}()
			}
			res, err := s.torrentManager.PrepareForPlayback(flightCtx, rel, season, episode,
				bufferBytes, profile, timeout, nil)
			fl.res, fl.err = res, err
			close(fl.done) // publishes res/err to everyone waiting
		}()
	} else {
		logger.Debug("Playback: joining the prepare already running for this torrent",
			"session", sessionID(sess), "title", releaseTitle(rel))
	}
	fl.waiters++
	g.mu.Unlock()

	var once sync.Once
	done := func() {
		once.Do(func() {
			g.mu.Lock()
			fl.waiters--
			last := fl.waiters == 0
			if last && g.inflight[key] == fl {
				delete(g.inflight, key)
			}
			g.mu.Unlock()
			if !last {
				return
			}
			// Nobody is left to use the result. Stop the prepare if it is still
			// running, then hand back the playback lease once it has stopped —
			// in the background, so an HTTP handler never blocks on it.
			fl.cancel()
			go func() {
				<-fl.done
				if fl.res != nil {
					s.torrentManager.ReleasePlayback(fl.res)
				}
			}()
		})
	}

	select {
	case <-ctx.Done():
		done()
		return nil, nil, ctx.Err()
	case <-fl.done:
	}

	if fl.err != nil {
		done()
		return nil, nil, fl.err
	}
	return fl.res, done, nil
}

func sessionID(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID
}

func releaseTitle(rel *release.Release) string {
	if rel == nil {
		return ""
	}
	return rel.Title
}
