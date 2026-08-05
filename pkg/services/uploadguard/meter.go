// Package uploadguard tracks how much a seedbox has uploaded during the current
// monthly billing period and reports whether the configured allowance has been
// reached. When it has, the seedbox provider typically throttles upload speed,
// so SeedStream uses this signal to serve only titles that can still stream
// smoothly and to hold heavier ones until the allowance resets.
//
// The total is a best-effort ESTIMATE, not the provider's authoritative meter:
// SeedStream cannot read the provider's counter. It sums two things it can see —
// BitTorrent upload reported by qBittorrent (folded in by the watchdog) and its
// own HTTP stream egress — so it will drift from the provider's number, but it
// is the closest approximation available without provider API access.
package uploadguard

import (
	"fmt"
	"sync"
	"time"

	"seedstream/pkg/core/logger"
)

// Store is the minimal persistence surface the meter needs.
// *persistence.StateManager satisfies it.
type Store interface {
	Get(key string, target interface{}) (bool, error)
	Set(key string, value interface{}) error
}

const stateKey = "upload_guard_meter"

// persistState is the meter's serialized form.
type persistState struct {
	// Period is the billing-cycle key (e.g. "2026-08") the totals belong to.
	Period string `json:"period"`
	// UploadedBytes is the running upload total for the current period.
	UploadedBytes int64 `json:"uploaded_bytes"`
	// SeedBaselines is the last-seen per-client qBittorrent session upload total,
	// used to fold in only the delta since the previous observation.
	SeedBaselines map[string]int64 `json:"seed_baselines"`
}

// Meter is a thread-safe monthly upload accumulator persisted through Store.
type Meter struct {
	store Store
	now   func() time.Time

	mu       sync.Mutex
	capBytes int64
	resetDay int
	st       persistState
}

// New loads any persisted state and returns a ready meter. now may be nil, in
// which case time.Now is used (tests inject a fixed clock).
func New(store Store, now func() time.Time) *Meter {
	if now == nil {
		now = time.Now
	}
	m := &Meter{store: store, now: now, resetDay: 1}
	m.st.SeedBaselines = map[string]int64{}
	if store != nil {
		var loaded persistState
		if ok, err := store.Get(stateKey, &loaded); err == nil && ok {
			if loaded.SeedBaselines == nil {
				loaded.SeedBaselines = map[string]int64{}
			}
			m.st = loaded
		} else if err != nil {
			logger.Warn("upload guard: failed to load persisted state, starting fresh", "err", err)
		}
	}
	return m
}

// SetLimits updates the cap (in bytes; 0 disables throttling) and the day of the
// month the allowance resets. Cheap to call repeatedly; callers refresh it from
// config on each use so config edits take effect without a restart.
func (m *Meter) SetLimits(capBytes int64, resetDay int) {
	if resetDay < 1 || resetDay > 28 {
		resetDay = 1
	}
	m.mu.Lock()
	m.capBytes = capBytes
	m.resetDay = resetDay
	m.mu.Unlock()
}

// billingPeriod returns the cycle key for now given a reset day. The cycle that
// contains a date starts on resetDay; before that day of the month the active
// cycle began in the previous month.
func billingPeriod(now time.Time, resetDay int) string {
	y, mo := now.Year(), now.Month()
	if now.Day() < resetDay {
		firstOfThisMonth := time.Date(y, mo, 1, 0, 0, 0, 0, now.Location())
		prev := firstOfThisMonth.AddDate(0, 0, -1)
		y, mo = prev.Year(), prev.Month()
	}
	return fmt.Sprintf("%04d-%02d", y, int(mo))
}

// rolloverLocked resets the totals when the billing period has changed.
// Caller must hold m.mu.
func (m *Meter) rolloverLocked() {
	period := billingPeriod(m.now(), m.resetDay)
	if m.st.Period == period {
		return
	}
	logger.Info("upload guard: new billing period, resetting monthly upload total",
		"old_period", m.st.Period, "new_period", period, "previous_total_gb", float64(m.st.UploadedBytes)/1e9)
	m.st.Period = period
	m.st.UploadedBytes = 0
	m.st.SeedBaselines = map[string]int64{}
}

// persistLocked writes the current state. Caller must hold m.mu.
func (m *Meter) persistLocked() {
	if m.store == nil {
		return
	}
	if err := m.store.Set(stateKey, m.st); err != nil {
		logger.Debug("upload guard: failed to persist state", "err", err)
	}
}

// AddStreamBytes records bytes SeedStream itself uploaded serving a stream.
// These are always incremental, so they are added directly.
func (m *Meter) AddStreamBytes(n int64) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	m.rolloverLocked()
	m.st.UploadedBytes += n
	m.persistLocked()
	m.mu.Unlock()
}

// RecordSeedingTotals folds in BitTorrent upload from qBittorrent. totals maps a
// client name to its current session upload counter (up_info_data). Only the
// positive delta since the previous observation is added; a counter that dropped
// means qBittorrent restarted, so the new session's bytes are counted from zero.
// The first observation of a client in a period sets a baseline only (nothing is
// added), so pre-existing session bytes that predate tracking are not counted.
func (m *Meter) RecordSeedingTotals(totals map[string]int64) {
	if len(totals) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rolloverLocked()
	for name, cur := range totals {
		if cur < 0 {
			continue
		}
		base, seen := m.st.SeedBaselines[name]
		switch {
		case !seen:
			// First look this period: anchor the baseline, count from here on.
		case cur >= base:
			m.st.UploadedBytes += cur - base
		default:
			// Session counter reset (qBittorrent restart): count from zero.
			m.st.UploadedBytes += cur
		}
		m.st.SeedBaselines[name] = cur
	}
	m.persistLocked()
}

// Used returns the current period's upload total in bytes.
func (m *Meter) Used() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rolloverLocked()
	return m.st.UploadedBytes
}

// Cap returns the configured cap in bytes (0 = disabled).
func (m *Meter) Cap() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capBytes
}

// Throttled reports whether the period's upload has reached the cap. Always
// false when the cap is 0 (feature disabled).
func (m *Meter) Throttled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rolloverLocked()
	return m.capBytes > 0 && m.st.UploadedBytes >= m.capBytes
}
