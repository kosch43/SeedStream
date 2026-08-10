package stremio

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/release"
	"seedstream/pkg/torrent"
)

// gatedQBittorrent is the e2e mock with a hold on /torrents/info, so a prepare
// can be parked mid-flight while a second request arrives. It also counts the
// polls, which is the quantity the duplicate loops actually cost: two loops
// meant two poll cycles and two re-anchor timers on one torrent.
func gatedQBittorrent(t *testing.T, saveDir, fileName string, fileSize int64, gate <-chan struct{}, polls *int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "test-sid"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v5.0.0")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(polls, 1) == 1 {
			<-gate // park the first prepare so the second request has to join it
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"hash":%q,"name":"Test Movie","size":%d,"progress":1.0,"state":"uploading","save_path":%q,"category":"seedstream","added_on":%d,"seeding_time":3600,"last_activity":%d,"ratio":1.5,"uploaded":%d}]`,
			e2eTestHash, fileSize, saveDir, time.Now().Unix()-60, time.Now().Unix(), fileSize)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"index":0,"name":%q,"size":%d,"progress":1.0,"priority":1}]`, fileName, fileSize)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (g *prepareGroup) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.inflight)
}

func (g *prepareGroup) waitersFor(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	fl, ok := g.inflight[key]
	if !ok {
		return 0
	}
	return fl.waiters
}

// TestConcurrentPreparesJoinOneFlight is the regression test for the duplicate
// prepare loops: a player opening two connections milliseconds apart got two
// independent prepare loops for one torrent, each with its own re-anchor timer,
// so the download client took twice the steering it was paced for.
func TestConcurrentPreparesJoinOneFlight(t *testing.T) {
	saveDir := t.TempDir()
	fileName := "Test.Movie.2024.1080p.mkv"
	content := make([]byte, 1<<20)
	if err := os.WriteFile(filepath.Join(saveDir, fileName), content, 0o644); err != nil {
		t.Fatalf("write test video: %v", err)
	}

	gate := make(chan struct{})
	var polls int64
	qbit := gatedQBittorrent(t, saveDir, fileName, int64(len(content)), gate, &polls)
	srv, sessMgr := newTorrentE2EServer(t, qbit.URL, saveDir)

	rel := &release.Release{
		Title:    "Test Movie 2024 1080p",
		Protocol: "torrent",
		InfoHash: e2eTestHash,
		Magnet:   "magnet:?xt=urn:btih:" + e2eTestHash,
	}
	slot := formatStreamSlotPath("default", "movie", "tt0000009", 0)
	if _, _, err := sessMgr.CreateDeferredSession(slot, rel, nil, "movie", "tt0000009", "Test Movie", "default"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess, err := sessMgr.GetSession(slot)
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}

	type outcome struct {
		res  *torrent.PrepareResult
		done func()
		err  error
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, done, err := srv.preparePlayback(context.Background(), sess, rel, e2eTestHash,
				0, 0, 1<<16, 20*time.Second)
			results[i] = outcome{res: res, done: done, err: err}
		}(i)
	}

	// Wait until both callers are attached to the same flight, then let the
	// parked prepare finish. If the second had started its own loop, the count
	// would never reach two.
	key := prepareKey(sess, rel, e2eTestHash)
	deadline := time.Now().Add(10 * time.Second)
	for srv.prepares.waitersFor(key) < 2 {
		if time.Now().After(deadline) {
			close(gate)
			wg.Wait()
			t.Fatalf("second request started its own prepare instead of joining; flights=%d", srv.prepares.size())
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(gate)
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("prepare %d failed: %v", i, r.err)
		}
		if r.res == nil {
			t.Fatalf("prepare %d returned no result", i)
		}
	}
	if results[0].res != results[1].res {
		t.Errorf("each caller got its own PrepareResult; they should share one prepare")
	}

	// The lease is held until the LAST caller is done. Releasing on the first
	// would demote the file's priority while the other is still streaming.
	results[0].done()
	if got := srv.prepares.waitersFor(key); got != 1 {
		t.Errorf("after one caller finished, waiters = %d, want 1", got)
	}
	results[1].done()
	if got := srv.prepares.size(); got != 0 {
		t.Errorf("flight not cleaned up after the last caller finished: %d entries", got)
	}

	// A later request is fresh work, not a stale cached result.
	res3, done3, err := srv.preparePlayback(context.Background(), sess, rel, e2eTestHash, 0, 0, 1<<16, 20*time.Second)
	if err != nil {
		t.Fatalf("prepare after release: %v", err)
	}
	defer done3()
	if res3 == results[0].res {
		t.Errorf("a request after the flight was cleaned up reused the old result")
	}
}

// TestPrepareSurvivesOneCallerLeaving: a player that opens a probe connection
// and drops it must not cancel the buffering the real request is waiting on.
func TestPrepareSurvivesOneCallerLeaving(t *testing.T) {
	saveDir := t.TempDir()
	fileName := "Test.Movie.2024.1080p.mkv"
	if err := os.WriteFile(filepath.Join(saveDir, fileName), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatalf("write test video: %v", err)
	}
	gate := make(chan struct{})
	var polls int64
	qbit := gatedQBittorrent(t, saveDir, fileName, 1<<20, gate, &polls)
	srv, sessMgr := newTorrentE2EServer(t, qbit.URL, saveDir)

	rel := &release.Release{Title: "Test Movie", Protocol: "torrent", InfoHash: e2eTestHash,
		Magnet: "magnet:?xt=urn:btih:" + e2eTestHash}
	slot := formatStreamSlotPath("default", "movie", "tt0000010", 0)
	if _, _, err := sessMgr.CreateDeferredSession(slot, rel, nil, "movie", "tt0000010", "Test Movie", "default"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess, _ := sessMgr.GetSession(slot)
	key := prepareKey(sess, rel, e2eTestHash)

	abandonCtx, abandon := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, done, err := srv.preparePlayback(abandonCtx, sess, rel, e2eTestHash, 0, 0, 1<<16, 20*time.Second)
		if err == nil {
			done()
			t.Error("the abandoned caller should have returned its context error")
		}
	}()

	// The stayer joins the same flight, then the prober walks away.
	stayer := make(chan *torrent.PrepareResult, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, done, err := srv.preparePlayback(context.Background(), sess, rel, e2eTestHash, 0, 0, 1<<16, 20*time.Second)
		if err != nil {
			t.Errorf("the remaining caller should still get its buffer: %v", err)
			stayer <- nil
			return
		}
		defer done()
		stayer <- res
	}()

	deadline := time.Now().Add(10 * time.Second)
	for srv.prepares.waitersFor(key) < 2 {
		if time.Now().After(deadline) {
			close(gate)
			abandon()
			wg.Wait()
			t.Fatal("callers never joined one flight")
		}
		time.Sleep(2 * time.Millisecond)
	}
	abandon()
	close(gate)

	select {
	case res := <-stayer:
		if res == nil {
			t.Fatal("the remaining caller lost its prepare when the other one left")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the remaining caller never got a result")
	}
	wg.Wait()
}
