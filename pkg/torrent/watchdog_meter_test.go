package torrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/persistence"
	"seedstream/pkg/services/cerberus"
	"seedstream/pkg/services/uploadguard"
)

// TestWatchdogMetersSeedingUpload proves the watchdog folds the seedbox's
// BitTorrent upload into the monthly upload guard and trips the throttle once
// the allowance is spent.
func TestWatchdogMetersSeedingUpload(t *testing.T) {
	var upTotal int64 = 1_000_000

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/transfer/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"up_info_data":%d,"dl_info_data":0,"up_info_speed":0}`, atomic.LoadInt64(&upTotal))
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "[]") // no torrents to health-check
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mgr := NewManager([]config.TorrentClientConfig{{
		Name: "box", Type: "qbittorrent", URL: srv.URL, Category: "seedstream",
	}})
	store, err := persistence.GetManager(t.TempDir())
	if err != nil {
		t.Fatalf("state manager: %v", err)
	}
	meter := uploadguard.New(nil, nil)
	cfg := &config.Config{MonthlyUploadCapGB: 0.002, UploadCapResetDay: 1} // cap = 2,000,000 bytes
	wd := NewWatchdog(mgr, cerberus.New(store), stubIndexer{}, cfg, meter)
	if wd == nil {
		t.Fatal("watchdog is nil")
	}

	ctx := context.Background()

	wd.check(ctx, 10*time.Minute) // first look: baseline only
	if got := meter.Used(); got != 0 {
		t.Fatalf("baseline observation should add nothing, used=%d want 0", got)
	}

	atomic.StoreInt64(&upTotal, 1_500_000)
	wd.check(ctx, 10*time.Minute) // +500,000
	if got := meter.Used(); got != 500_000 {
		t.Fatalf("seeding delta not metered, used=%d want 500000", got)
	}
	if meter.Throttled() {
		t.Fatalf("500k is under the 2,000,000 cap — should not be throttled")
	}

	atomic.StoreInt64(&upTotal, 3_000_000)
	wd.check(ctx, 10*time.Minute) // +1,500,000 => 2,000,000, meets cap
	if !meter.Throttled() {
		t.Fatalf("used=%d should have reached the cap and throttled", meter.Used())
	}
}
