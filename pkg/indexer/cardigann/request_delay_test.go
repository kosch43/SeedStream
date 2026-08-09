package cardigann

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRequestDelayIsParsed: the field was absent from the schema entirely, so
// the YAML decoder dropped it silently and every tracker that asks for a delay
// was treated as if it had asked for none.
func TestRequestDelayIsParsed(t *testing.T) {
	def, err := Parse([]byte(`
id: t
name: T
type: private
requestDelay: 4.1
links:
  - https://example.invalid/
search:
  paths:
    - path: browse.php
  rows:
    selector: table tr
  fields:
    title:
      selector: a
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if def.RequestDelay != 4.1 {
		t.Fatalf("requestDelay = %v, want 4.1", def.RequestDelay)
	}
}

// TestBundledDefinitionsCarryTheirDelay proves this against the real catalogue
// rather than a hand-written stub: TorrentLeech asks for 4.1 seconds, and that
// has to survive loading.
func TestBundledDefinitionsCarryTheirDelay(t *testing.T) {
	cat := NewCatalog("")
	def, ok := cat.Get("torrentleech")
	if !ok {
		t.Skip("torrentleech definition not bundled")
	}
	if def.RequestDelay <= 0 {
		t.Fatalf("TorrentLeech declares requestDelay 4.1; got %v", def.RequestDelay)
	}
}

// TestRequestsAreSpacedByTheDeclaredDelay is the behaviour that protects the
// account. Without it a tracker asking for seconds between requests receives
// them back to back, which is how a private tracker rate-limits or bans.
func TestRequestsAreSpacedByTheDeclaredDelay(t *testing.T) {
	e := &Engine{def: &Definition{RequestDelay: 0.2}}
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := e.waitForTurn(ctx); err != nil {
			t.Fatalf("waitForTurn: %v", err)
		}
	}
	// First is free; the next two each wait 200ms.
	if elapsed := time.Since(start); elapsed < 380*time.Millisecond {
		t.Fatalf("three requests took %v — the declared delay was not applied", elapsed)
	}
}

// TestConcurrentSearchesQueueRatherThanBurst is why the lock is held across the
// wait. Searches now run in parallel, so several requests reach the same
// tracker at once; if each merely read the timestamp and returned, they would
// all see the same stale value and fire together — exactly the burst the delay
// exists to prevent.
func TestConcurrentSearchesQueueRatherThanBurst(t *testing.T) {
	e := &Engine{def: &Definition{RequestDelay: 0.15}}

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.waitForTurn(context.Background())
		}()
	}
	wg.Wait()

	// Four requests at 150ms spacing: the first is free, three wait.
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("four concurrent requests completed in %v — they burst instead of queueing", elapsed)
	}
}

// TestNoDelayMeansNoWait: most definitions declare nothing, and those must not
// be slowed down.
func TestNoDelayMeansNoWait(t *testing.T) {
	e := &Engine{def: &Definition{}}
	start := time.Now()
	for i := 0; i < 50; i++ {
		if err := e.waitForTurn(context.Background()); err != nil {
			t.Fatalf("waitForTurn: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("a tracker declaring no delay must not be throttled, took %v", elapsed)
	}
}

// TestWaitIsCancellable: a viewer who gives up must not leave a request parked
// on a multi-second delay holding the queue.
func TestWaitIsCancellable(t *testing.T) {
	e := &Engine{def: &Definition{RequestDelay: 30}}
	if err := e.waitForTurn(context.Background()); err != nil {
		t.Fatalf("first call should not wait: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := e.waitForTurn(ctx); err == nil {
		t.Fatal("a cancelled context must abort the wait")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %v — it should be immediate", elapsed)
	}
}
