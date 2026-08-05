package stremio

import (
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
	"seedstream/pkg/services/uploadguard"
	"seedstream/pkg/session"
)

func movieSession(sizeBytes int64) *session.Session {
	return &session.Session{
		ID:         "test",
		Release:    &release.Release{Size: sizeBytes},
		ContentIDs: &session.ContentMeta{ImdbID: "tt0000000"},
	}
}

// guardServer builds a Server wired with a meter whose usage already exceeds the
// given cap (in bytes) so the guard is throttled. tmdbClient is left nil, so the
// guard falls back to the nominal runtime.
func guardServer(t *testing.T, capBytes int64, throttled bool) *Server {
	t.Helper()
	meter := uploadguard.New(nil, func() time.Time { return time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) })
	meter.SetLimits(capBytes, 1)
	if throttled {
		meter.AddStreamBytes(capBytes + 1)
	}
	// MonthlyUploadCapGB is applied by applyUploadGuard via SetLimits, so it must
	// match capBytes (cap is decimal GB -> bytes).
	cfg := &config.Config{
		MonthlyUploadCapGB: float64(capBytes) / 1e9,
		PostCapUploadMbps:  10,
		UploadCapResetDay:  1,
	}
	return &Server{config: cfg, uploadMeter: meter}
}

func TestUploadGuardDisabledReturnsZero(t *testing.T) {
	s := &Server{config: &config.Config{}, uploadMeter: uploadguard.New(nil, nil)}
	buf, err := s.applyUploadGuard(movieSession(20_000_000_000))
	if err != nil || buf != 0 {
		t.Fatalf("disabled guard should be a no-op, buf=%d err=%v", buf, err)
	}
}

func TestUploadGuardNotThrottledReturnsZero(t *testing.T) {
	s := guardServer(t, 1_000_000, false) // usage below cap
	buf, err := s.applyUploadGuard(movieSession(20_000_000_000))
	if err != nil || buf != 0 {
		t.Fatalf("under-cap guard should be a no-op, buf=%d err=%v", buf, err)
	}
}

func TestUploadGuardHoldsHeavyTitle(t *testing.T) {
	s := guardServer(t, 1_000, true)
	// 20 GB over a nominal 2h runtime ≈ 22 Mbps, well over the 10 Mbps throttle.
	_, err := s.applyUploadGuard(movieSession(20_000_000_000))
	if err == nil {
		t.Fatalf("a 22 Mbps title must be held while throttled to 10 Mbps")
	}
}

func TestUploadGuardServesLightTitleWithBiggerBuffer(t *testing.T) {
	s := guardServer(t, 1_000, true)
	// 3 GB over a nominal 2h runtime ≈ 3.3 Mbps, comfortably under 10 Mbps.
	buf, err := s.applyUploadGuard(movieSession(3_000_000_000))
	if err != nil {
		t.Fatalf("a 3.3 Mbps title should be served, got hold error: %v", err)
	}
	if buf <= 0 {
		t.Fatalf("served title should get an enlarged head buffer, buf=%d", buf)
	}
}
