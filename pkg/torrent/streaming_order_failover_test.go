package torrent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"seedstream/pkg/core/config"
)

// orderQBit is a qBittorrent whose streaming-order toggles fail, leaving the
// flags unconfirmable. Everything else about it works.
func orderQBit(t *testing.T, togglesWork bool) *Manager {
	t.Helper()
	const hash = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	seq, flpp := false, false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":"%s","name":"Thing.S01E01.1080p","size":%d,"progress":0.1,"state":"downloading","category":"seedstream","save_path":"/downloads","num_seeds":50,"num_complete":50,"seq_dl":%t,"f_l_piece_prio":%t}]`,
			hash, 16<<20, seq, flpp)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"index":0,"name":"Thing.S01E01.1080p.mkv","size":%d,"progress":0.1,"priority":1,"piece_range":[0,15]}]`, 16<<20)
	})
	mux.HandleFunc("/api/v2/torrents/properties", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"piece_size":%d,"pieces_num":16,"total_size":%d}`, 1<<20, 16<<20)
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "[2,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]")
	})
	toggle := func(target *bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !togglesWork {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			*target = true
		}
	}
	mux.HandleFunc("/api/v2/torrents/toggleSequentialDownload", toggle(&seq))
	mux.HandleFunc("/api/v2/torrents/toggleFirstLastPiecePrio", toggle(&flpp))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: srv.URL, Category: "seedstream",
	}})
}

// TestStreamingOrderFailureKeepsTheRelease is the interaction that matters.
// Refusing to start a player on a torrent whose ordering cannot be confirmed is
// right, but the obstacle is the download client, not the release — so handing
// the next candidate to the same client fails identically. Classified as a
// failed candidate, the fallback loop walks the whole list and starts a
// download for each, which is the several-copies-of-one-film pathology.
func TestStreamingOrderFailureKeepsTheRelease(t *testing.T) {
	mgr := orderQBit(t, false)

	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		8<<20, PlaybackProfile{}, 20*time.Second, nil)
	if err == nil {
		t.Fatal("prepare must fail when streaming order cannot be confirmed")
	}
	if !errors.Is(err, ErrStreamingOrderUnavailable) {
		t.Fatalf("error should name the cause, got: %v", err)
	}
	if !KeepReleaseOnRetry(err) {
		t.Fatalf("this must not send the next attempt to a different release, got: %v", err)
	}
	// The underlying client error stays readable through the wrapping.
	if !contains(err.Error(), "toggleSequentialDownload") {
		t.Errorf("the client's own error should survive wrapping, got: %v", err)
	}
}

// TestKeepReleaseOnRetryCoversBothCauses: the failover loop asks one question —
// could a different candidate do better? Both answers of "no" belong here, and
// everything that genuinely is a bad release must stay outside it.
func TestKeepReleaseOnRetryCoversBothCauses(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("%w: after 90s the head is short", ErrStillBuffering),
		fmt.Errorf("%w: HTTP 500", ErrStreamingOrderUnavailable),
	} {
		if !KeepReleaseOnRetry(err) {
			t.Errorf("%v should keep the release", err)
		}
	}
	for _, err := range []error{
		errors.New("torrent contains no playable video file (3 files)"),
		errors.New("swarm has 3 seeder(s), below the 10 required to stream"),
		errors.New("release does not match the requested content"),
		nil,
	} {
		if KeepReleaseOnRetry(err) {
			t.Errorf("%v is a failed candidate and must fail over", err)
		}
	}
}

// TestStreamingOrderSucceedsWhenTogglesWork keeps the happy path honest: when
// the flags can be set, prepare proceeds past this check rather than being
// blocked by it.
func TestStreamingOrderSucceedsWhenTogglesWork(t *testing.T) {
	mgr := orderQBit(t, true)

	_, err := mgr.PrepareForPlayback(context.Background(), prepareTestRelease(), 1, 1,
		8<<20, PlaybackProfile{}, 6*time.Second, nil)
	if err == nil {
		t.Fatal("expected a buffering timeout, since the head never fills")
	}
	if errors.Is(err, ErrStreamingOrderUnavailable) {
		t.Fatalf("streaming order was enabled successfully; it must not be blamed: %v", err)
	}
}
