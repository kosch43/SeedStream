package torrent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"seedstream/pkg/torrent/qbittorrent"
	"seedstream/pkg/torrent/tclient"
)

type priorityQBit struct {
	mu         sync.Mutex
	priorities []int
}

func (q *priorityQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		defer q.mu.Unlock()
		files := []tclient.FileInfo{
			{Index: 0, Name: "Show.S01E01.mkv", Size: 100, Priority: q.priorities[0], PieceRange: []int{0, 9}},
			{Index: 1, Name: "Show.S01E02.mkv", Size: 100, Priority: q.priorities[1], PieceRange: []int{10, 19}},
		}
		if err := json.NewEncoder(w).Encode(files); err != nil {
			t.Errorf("encode files: %v", err)
		}
	})
	mux.HandleFunc("/api/v2/torrents/filePrio", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse priority form: %v", err)
			return
		}
		idx, err := strconv.Atoi(r.PostForm.Get("id"))
		if err != nil {
			t.Errorf("parse file index: %v", err)
			return
		}
		priority, err := strconv.Atoi(r.PostForm.Get("priority"))
		if err != nil {
			t.Errorf("parse priority: %v", err)
			return
		}
		q.mu.Lock()
		q.priorities[idx] = priority
		q.mu.Unlock()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (q *priorityQBit) snapshot() []int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]int(nil), q.priorities...)
}

func TestSelectedVideoPriorityReplacesPreviousEpisode(t *testing.T) {
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	q := &priorityQBit{priorities: []int{7, 1}}
	server := q.server(t)
	client := qbittorrent.New(qbittorrent.Options{BaseURL: server.URL})
	mgr := NewManager(nil)
	key := playbackSelectionKey("box", hash)
	release := mgr.beginPlaybackSelection(key, 1)
	defer mgr.releasePlaybackSelectionNow(client, key, hash, release)

	if err := mgr.prioritizeActiveVideoFiles(context.Background(), client, key, hash); err != nil {
		t.Fatalf("prioritize selected video: %v", err)
	}
	got := q.snapshot()
	if got[0] != 7 || got[1] != 7 {
		t.Fatalf("file priorities = %v, want [7 7] (preserve unmanaged priority)", got)
	}
}

func TestConcurrentEpisodePrioritiesKeepBothActive(t *testing.T) {
	const hash = "bcdefabcdefabcdefabcdefabcdefabcdefabcde"
	q := &priorityQBit{priorities: []int{1, 1}}
	server := q.server(t)
	clients := []*qbittorrent.Client{
		qbittorrent.New(qbittorrent.Options{BaseURL: server.URL}),
		qbittorrent.New(qbittorrent.Options{BaseURL: server.URL}),
	}
	mgr := NewManager(nil)
	key := playbackSelectionKey("box", hash)
	releaseFirst := mgr.beginPlaybackSelection(key, 0)
	releaseSecond := mgr.beginPlaybackSelection(key, 1)
	defer mgr.releasePlaybackSelectionNow(clients[0], key, hash, releaseSecond)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, client := range clients {
		wg.Add(1)
		go func(client *qbittorrent.Client) {
			defer wg.Done()
			errs <- mgr.prioritizeActiveVideoFiles(context.Background(), client, key, hash)
		}(client)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("prioritize selected video: %v", err)
		}
	}

	got := q.snapshot()
	if got[0] != 7 || got[1] != 7 {
		t.Fatalf("concurrent active priorities = %v, want [7 7]", got)
	}

	releaseFirst()
	if err := mgr.prioritizeActiveVideoFiles(context.Background(), clients[0], key, hash); err != nil {
		t.Fatalf("normalize after first stream ended: %v", err)
	}
	got = q.snapshot()
	if got[0] != 1 || got[1] != 7 {
		t.Fatalf("priorities after first stream ended = %v, want [1 7]", got)
	}
}
