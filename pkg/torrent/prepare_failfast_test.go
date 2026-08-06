package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
)

// prepareQBit is a mock qBittorrent whose /torrents/files behaviour is
// configurable, so the prepare loop's failure handling can be exercised.
type prepareQBit struct {
	filesStatus int    // non-200 makes Files() error
	filesBody   string // used when filesStatus is 200
}

func (q *prepareQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%s","name":"Thing","size":1000,"progress":1,"state":"uploading","category":"seedstream","save_path":"/downloads"}]`, hash)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		if q.filesStatus != 0 && q.filesStatus != http.StatusOK {
			w.WriteHeader(q.filesStatus)
			return
		}
		fmt.Fprint(w, q.filesBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func prepareTestRelease() *release.Release {
	return &release.Release{
		Title:    "Thing.S01E01.1080p",
		Protocol: "torrent",
		InfoHash: "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Magnet:   "magnet:?xt=urn:btih:abcdefabcdefabcdefabcdefabcdefabcdefabcd",
	}
}

// TestPrepareFailsFastOnClientError: when the download client keeps erroring,
// prepare must surface that error quickly instead of spinning for the whole
// timeout and blaming buffering. The slow path is what pushed a play request
// past an upstream proxy's ceiling.
func TestPrepareFailsFastOnClientError(t *testing.T) {
	q := &prepareQBit{filesStatus: http.StatusForbidden}
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})

	start := time.Now()
	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 0, PlaybackProfile{}, 30*time.Second, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a persistently failing client must produce an error")
	}
	// 3 attempts at 1.5s apart is ~3s; it must not consume the 30s timeout.
	if elapsed > 15*time.Second {
		t.Fatalf("prepare took %v — it should fail fast on client errors, not wait out the timeout", elapsed)
	}
	if !contains(err.Error(), "unreachable") {
		t.Fatalf("error should name the client failure, got: %v", err)
	}
}

// TestPrepareFailsFastWhenNoVideoFile: if the torrent's file list is known and
// holds no playable video, waiting cannot help, so prepare must return at once.
func TestPrepareFailsFastWhenNoVideoFile(t *testing.T) {
	q := &prepareQBit{
		filesStatus: http.StatusOK,
		filesBody:   `[{"index":0,"name":"readme.txt","size":100,"progress":1,"piece_range":[0,0]}]`,
	}
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})

	start := time.Now()
	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 0, PlaybackProfile{}, 30*time.Second, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a torrent with no video file must produce an error")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("prepare took %v — a file list with no video should fail immediately", elapsed)
	}
	if !contains(err.Error(), "no playable video") {
		t.Fatalf("error should explain there is no video file, got: %v", err)
	}
}

// TestPrepareReturnsImmediatelyOnCompleteFile: the happy path must not regress —
// an already-complete video returns without waiting.
func TestPrepareReturnsImmediatelyOnCompleteFile(t *testing.T) {
	q := &prepareQBit{
		filesStatus: http.StatusOK,
		filesBody:   `[{"index":0,"name":"Thing.S01E01.1080p.mkv","size":1000,"progress":1,"piece_range":[0,3]}]`,
	}
	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: q.server(t).URL, Category: "seedstream",
	}})

	start := time.Now()
	res, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1, 0, PlaybackProfile{}, 30*time.Second, nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a complete file should prepare successfully: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("a complete file took %v to prepare — should be immediate", elapsed)
	}
	if res.Name != "Thing.S01E01.1080p.mkv" {
		t.Fatalf("unexpected file %q", res.Name)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
