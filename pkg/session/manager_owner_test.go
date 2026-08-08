package session

import (
	"testing"
	"time"

	"seedstream/pkg/release"
)

func TestGetSessionForStreamRejectsForeignOwner(t *testing.T) {
	mgr := NewManager(time.Minute)
	defer mgr.Shutdown()

	const slot = "stream:alice:movie:tt123:0"
	if _, _, err := mgr.CreateDeferredSession(slot, &release.Release{Title: "Movie", Link: "magnet:?xt=urn:btih:one"}, nil, "movie", "tt123", "Movie", "alice"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := mgr.GetSessionForStream(slot, "bob"); err == nil {
		t.Fatal("expected foreign stream lookup to fail")
	}
	if got, err := mgr.GetSessionForStream(slot, "alice"); err != nil || got == nil {
		t.Fatalf("owner lookup failed: session=%v err=%v", got, err)
	}
}

func TestCreateDeferredSessionRejectsForeignReplacement(t *testing.T) {
	mgr := NewManager(time.Minute)
	defer mgr.Shutdown()

	const slot = "stream:alice:movie:tt123:0"
	rel := &release.Release{Title: "Movie", Link: "magnet:?xt=urn:btih:one"}
	if _, _, err := mgr.CreateDeferredSession(slot, rel, nil, "movie", "tt123", "Movie", "alice"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := mgr.CreateDeferredSession(slot, rel, nil, "movie", "tt123", "Movie", "bob"); err == nil {
		t.Fatal("expected foreign stream replacement to fail")
	}
}

func TestFailoverOrderLookupIsOwnerQualified(t *testing.T) {
	mgr := NewManager(time.Minute)
	defer mgr.Shutdown()

	order := []string{"stream:alice:movie:tt123:0"}
	mgr.SetStreamFailoverOrderForStream("token", "alice", "alice:movie:tt123", order)
	if got := mgr.GetStreamFailoverOrderForStream("token", "alice", "alice:movie:tt123"); len(got) != 1 || got[0] != order[0] {
		t.Fatalf("owner order = %#v", got)
	}
	if got := mgr.GetStreamFailoverOrderForStream("token", "bob", "alice:movie:tt123"); got != nil {
		t.Fatalf("foreign owner received order %#v", got)
	}
}
