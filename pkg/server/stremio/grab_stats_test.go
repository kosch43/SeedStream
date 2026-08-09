package stremio

import (
	"sync"
	"testing"
	"time"

	"seedstream/pkg/release"
	"seedstream/pkg/session"
	"seedstream/pkg/stats"
)

// countingRecorder captures what was recorded so the numbers can be asserted
// rather than merely the absence of a panic.
type countingRecorder struct {
	mu        sync.Mutex
	downloads []recordedDownload
}

type recordedDownload struct {
	name               string
	success            bool
	indexersInSearch   int
	indexersWithResult int
}

func (r *countingRecorder) RecordIndexerSearch(string, bool, int64, int) {}

// Only the download path is under test here; the rest of the interface is
// satisfied so the recorder can stand in for the real one.
func (r *countingRecorder) RecordProviderDelta(string, string, int64, int64, int64) {}

func (r *countingRecorder) RecordIndexerDownload(name string, success bool, in, with int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.downloads = append(r.downloads, recordedDownload{name, success, in, with})
}

func (r *countingRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.downloads)
}

// withRecorder installs a capturing recorder for one test and restores the
// previous one afterwards, so tests do not leak into each other.
func withRecorder(t *testing.T) *countingRecorder {
	t.Helper()
	rec := &countingRecorder{}
	stats.SetDefault(rec)
	t.Cleanup(func() { stats.SetDefault(stats.NopRecorder{}) })
	return rec
}

// grabSession builds a real session through the manager's own API, so the
// latch under test is the one production uses rather than a stand-in.
func grabSession(t *testing.T, indexerName string) *session.Session {
	t.Helper()
	return grabSessionWithRelease(t, "slot-1", &release.Release{
		Title: "Some.Film.2024.1080p-GRP", Protocol: "torrent",
		Indexer: indexerName, IndexersInSearch: 4, IndexersWithResult: 2,
	})
}

func grabSessionWithRelease(t *testing.T, id string, rel *release.Release) *session.Session {
	t.Helper()
	mgr := session.NewManager(time.Hour)
	t.Cleanup(mgr.Shutdown)
	sess, _, err := mgr.CreateDeferredSession(id, rel, nil, "movie", "tt1", "Some Film", "default")
	if err != nil {
		t.Fatalf("CreateDeferredSession: %v", err)
	}
	return sess
}

// TestGrabIsRecorded is Bugs 3 and 4. Nothing counted a grab anywhere: the only
// code that did lives in the Usenet download path, which a torrent never
// reaches, so every tracker's download column was structurally zero.
func TestGrabIsRecorded(t *testing.T) {
	rec := withRecorder(t)
	sess := grabSession(t, "MyTracker")

	recordTrackerGrab(sess, sess.Release, nil)

	if rec.count() != 1 {
		t.Fatalf("expected one recorded grab, got %d", rec.count())
	}
	got := rec.downloads[0]
	if got.name != "MyTracker" {
		t.Errorf("recorded against %q, want MyTracker", got.name)
	}
	if !got.success {
		t.Error("a grab that reached a download client is a success")
	}
	if got.indexersInSearch != 4 || got.indexersWithResult != 2 {
		t.Errorf("uniqueness figures not carried through: got %d/%d, want 4/2",
			got.indexersInSearch, got.indexersWithResult)
	}
}

// TestGrabIsRecordedOnlyOncePerSession is the counting hazard. A player
// reconnects and re-requests ranges throughout playback, and every one of those
// re-enters the play handler — an unguarded counter would report dozens of
// grabs for a single torrent and make the page useless.
func TestGrabIsRecordedOnlyOncePerSession(t *testing.T) {
	rec := withRecorder(t)
	sess := grabSession(t, "MyTracker")

	for i := 0; i < 25; i++ {
		recordTrackerGrab(sess, sess.Release, nil)
	}
	if rec.count() != 1 {
		t.Fatalf("one torrent is one grab however many times playback re-enters, got %d", rec.count())
	}
}

// TestConcurrentPlaysRecordOneGrab: two requests can enter playback for the
// same session at once. The latch has to be atomic, not merely a check.
func TestConcurrentPlaysRecordOneGrab(t *testing.T) {
	rec := withRecorder(t)
	sess := grabSession(t, "MyTracker")

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordTrackerGrab(sess, sess.Release, nil)
		}()
	}
	wg.Wait()

	if rec.count() != 1 {
		t.Fatalf("concurrent entries must still record exactly one grab, got %d", rec.count())
	}
}

// TestGrabWithoutATrackerIsNotRecorded: a release carrying no tracker would be
// counted against an empty name, inventing a phantom tracker on the page.
func TestGrabWithoutATrackerIsNotRecorded(t *testing.T) {
	rec := withRecorder(t)
	sess := grabSessionWithRelease(t, "slot-2", &release.Release{
		Title: "Orphan", Protocol: "torrent",
	})

	recordTrackerGrab(sess, sess.Release, nil)

	if rec.count() != 0 {
		t.Fatalf("a release with no tracker must not create one, got %d records", rec.count())
	}
}

// TestGrabFallsBackToTheSessionsTracker: replaying from a torrent the seedbox
// already holds substitutes a different release, and that copy carries no
// tracker of its own. The session's original release is the honest attribution
// — that is where the content was actually found.
func TestGrabFallsBackToTheSessionsTracker(t *testing.T) {
	rec := withRecorder(t)
	sess := grabSession(t, "OriginalTracker")

	// What resolvePlayRelease hands back: same content, no tracker attached.
	reused := &release.Release{Title: "Some.Other.Copy", Protocol: "torrent"}
	recordTrackerGrab(sess, reused, nil)

	if rec.count() != 1 {
		t.Fatalf("expected one grab, got %d", rec.count())
	}
	if got := rec.downloads[0].name; got != "OriginalTracker" {
		t.Fatalf("attributed to %q, want the tracker the content was found on", got)
	}
}

// TestGrabSurvivesNilInputs: the recorder sits on the playback path and must
// never be the reason a stream fails.
func TestGrabSurvivesNilInputs(t *testing.T) {
	withRecorder(t)
	recordTrackerGrab(nil, nil, nil)
	recordTrackerGrab(grabSession(t, "T"), nil, nil)
	recordTrackerGrab(nil, &release.Release{Indexer: "T"}, nil)
}
