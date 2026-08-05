package uploadguard

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory Store that round-trips values through JSON, the same
// way the real StateManager does.
type fakeStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (f *fakeStore) Get(key string, target interface{}) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, target)
}

func (f *fakeStore) Set(key string, value interface{}) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string][]byte{}
	}
	f.m[key] = b
	return nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func newTestMeter(t *testing.T, at time.Time) (*Meter, *fakeStore, *fakeClock) {
	t.Helper()
	store := &fakeStore{}
	clk := &fakeClock{t: at}
	return New(store, clk.now), store, clk
}

// TestSeedingBaselineThenDelta: the first observation of a client only sets a
// baseline; subsequent observations add the positive delta.
func TestSeedingBaselineThenDelta(t *testing.T) {
	m, _, _ := newTestMeter(t, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))

	m.RecordSeedingTotals(map[string]int64{"box": 100})
	if got := m.Used(); got != 0 {
		t.Fatalf("first observation should set baseline only, used=%d want 0", got)
	}
	m.RecordSeedingTotals(map[string]int64{"box": 250})
	if got := m.Used(); got != 150 {
		t.Fatalf("delta not folded, used=%d want 150", got)
	}
	m.RecordSeedingTotals(map[string]int64{"box": 400})
	if got := m.Used(); got != 300 {
		t.Fatalf("second delta not folded, used=%d want 300", got)
	}
}

// TestSeedingCounterResetCountsFromZero: when qBittorrent restarts, its session
// counter drops; the new session's bytes are counted from zero.
func TestSeedingCounterResetCountsFromZero(t *testing.T) {
	m, _, _ := newTestMeter(t, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))

	m.RecordSeedingTotals(map[string]int64{"box": 100}) // baseline
	m.RecordSeedingTotals(map[string]int64{"box": 500}) // +400 => 400
	m.RecordSeedingTotals(map[string]int64{"box": 50})  // restart: +50 => 450
	if got := m.Used(); got != 450 {
		t.Fatalf("restart handling wrong, used=%d want 450", got)
	}
}

// TestStreamBytesAndThrottle: stream egress accumulates and trips the cap.
func TestStreamBytesAndThrottle(t *testing.T) {
	m, _, _ := newTestMeter(t, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	m.SetLimits(500, 1)

	m.AddStreamBytes(400)
	if m.Throttled() {
		t.Fatalf("should not be throttled at 400/500")
	}
	m.AddStreamBytes(200)
	if got := m.Used(); got != 600 {
		t.Fatalf("stream bytes not summed, used=%d want 600", got)
	}
	if !m.Throttled() {
		t.Fatalf("should be throttled at 600/500")
	}
}

// TestDisabledCapNeverThrottles: cap 0 means the feature is off.
func TestDisabledCapNeverThrottles(t *testing.T) {
	m, _, _ := newTestMeter(t, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	m.SetLimits(0, 1)
	m.AddStreamBytes(1 << 40)
	if m.Throttled() {
		t.Fatalf("cap 0 must never throttle")
	}
}

// TestRolloverResetsTotal: crossing into a new billing period zeroes the total.
func TestRolloverResetsTotal(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	m := New(&fakeStore{}, clk.now)
	m.SetLimits(1000, 1)

	m.AddStreamBytes(800)
	if got := m.Used(); got != 800 {
		t.Fatalf("used=%d want 800", got)
	}
	// Move into September (reset day 1).
	clk.set(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if got := m.Used(); got != 0 {
		t.Fatalf("period rollover should reset total, used=%d want 0", got)
	}
	// A stale seeding baseline must not resurrect old bytes.
	m.RecordSeedingTotals(map[string]int64{"box": 999})
	if got := m.Used(); got != 0 {
		t.Fatalf("baseline after rollover should add nothing, used=%d want 0", got)
	}
}

// TestBillingPeriodResetDay: before the reset day the active cycle belongs to
// the previous month.
func TestBillingPeriodResetDay(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		day      int
		resetDay int
		want     string
	}{
		{10, 15, "2026-07"}, // before reset day -> previous month
		{15, 15, "2026-08"}, // on reset day -> this month
		{20, 15, "2026-08"},
		{1, 1, "2026-08"},
		{5, 1, "2026-08"},
	}
	for _, c := range cases {
		got := billingPeriod(time.Date(2026, 8, c.day, 0, 0, 0, 0, loc), c.resetDay)
		if got != c.want {
			t.Errorf("billingPeriod(day=%d,reset=%d)=%q want %q", c.day, c.resetDay, got, c.want)
		}
	}
}

// TestPersistenceRoundTrip: a fresh meter loads the persisted total.
func TestPersistenceRoundTrip(t *testing.T) {
	store := &fakeStore{}
	clk := &fakeClock{t: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	m1 := New(store, clk.now)
	m1.SetLimits(10000, 1)
	m1.AddStreamBytes(1234)
	m1.RecordSeedingTotals(map[string]int64{"box": 100})
	m1.RecordSeedingTotals(map[string]int64{"box": 400}) // +300 => 1534

	m2 := New(store, clk.now)
	m2.SetLimits(10000, 1)
	if got := m2.Used(); got != 1534 {
		t.Fatalf("reloaded total wrong, used=%d want 1534", got)
	}
	// The reloaded meter keeps the baseline, so it folds only new deltas.
	m2.RecordSeedingTotals(map[string]int64{"box": 500}) // +100 => 1634
	if got := m2.Used(); got != 1634 {
		t.Fatalf("reloaded baseline not honored, used=%d want 1634", got)
	}
}
