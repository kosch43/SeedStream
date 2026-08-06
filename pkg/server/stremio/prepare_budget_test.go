package stremio

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/torrent"
)

func budgetServer(timeoutSec int) *Server {
	return &Server{config: &config.Config{TorrentPrepareTimeoutSeconds: timeoutSec}}
}

// TestPrepareBudgetScalesWithTheHead is the fix for the observed timeout. The
// fixed 90 seconds was sized when every head was 16 MiB; a 4K remux asks for
// 402 MB, which at any plausible rate does not arrive in that window, so the
// request expired every time and the failover started a second copy.
func TestPrepareBudgetScalesWithTheHead(t *testing.T) {
	s := budgetServer(90)
	base := 90 * time.Second

	// An ordinary 1080p head is fractions of a second of download; the budget
	// should be indistinguishable from the configured one.
	if got := s.prepareBudget(nil, 4<<20); got > base+2*time.Second {
		t.Fatalf("a 4 MiB head should barely move the budget, got %v against a base of %v", got, base)
	}
	big := s.prepareBudget(nil, 402<<20)
	if big <= base {
		t.Fatalf("a 402 MiB head must extend the budget, got %v", big)
	}
	if big > maxPrepareBudget {
		t.Fatalf("budget %v exceeds the cap %v", big, maxPrepareBudget)
	}
}

// TestPrepareBudgetIsCapped: the budget is spent inside one HTTP request, and
// reverse proxies cut those off. Extending without limit would trade a timeout
// for a proxy error, which is worse because the retry loses the explanation.
func TestPrepareBudgetIsCapped(t *testing.T) {
	s := budgetServer(90)
	if got := s.prepareBudget(nil, 100<<30); got != maxPrepareBudget {
		t.Fatalf("an absurd head must clamp to %v, got %v", maxPrepareBudget, got)
	}
}

// TestPrepareBudgetHonoursAHigherSetting: the configured timeout is a floor,
// never a ceiling. An operator who raised it meant it.
func TestPrepareBudgetHonoursAHigherSetting(t *testing.T) {
	s := budgetServer(150)
	if got := s.prepareBudget(nil, 0); got != 150*time.Second {
		t.Fatalf("an explicit 150s must be honoured, got %v", got)
	}
	if got := s.prepareBudget(nil, 16<<20); got < 150*time.Second {
		t.Fatalf("the configured timeout must never be reduced, got %v", got)
	}
}

// TestStillBufferingIsDistinguishable is what stops the duplicate download. The
// failover loop has to tell "this release cannot work" from "this release needs
// longer" — the first deserves another candidate, the second must not get one,
// because a second candidate means a second 59 GB torrent for the same film
// competing for the same bandwidth.
func TestStillBufferingIsDistinguishable(t *testing.T) {
	wrapped := fmt.Errorf("torrent still preparing: %w",
		fmt.Errorf("%w: after 90s the file is 40.5%% downloaded and needs 402653184 bytes of head",
			torrent.ErrStillBuffering))
	if !errors.Is(wrapped, torrent.ErrStillBuffering) {
		t.Fatal("a buffering timeout must remain identifiable through the handler's wrapping")
	}

	// A release that genuinely cannot work must stay distinguishable from it.
	for _, err := range []error{
		errors.New("torrent contains no playable video file (3 files)"),
		errors.New("swarm has 3 seeder(s), below the 10 required to stream"),
		errors.New("release does not match the requested content"),
	} {
		if errors.Is(err, torrent.ErrStillBuffering) {
			t.Fatalf("%q is a failed candidate, not a slow one", err)
		}
	}
}
