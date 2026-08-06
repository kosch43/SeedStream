package torrent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"seedstream/pkg/torrent/qbittorrent"
)

// estimateQBit reports progress but no piece_range, forcing the fallback path.
type estimateQBit struct{ progress float64 }

func (q *estimateQBit) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	// No piece_range: an older client that cannot say where bytes are.
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"index":0,"name":"v.mkv","size":8388608,"progress":%v}]`, q.progress)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestEstimateNeverClaimsUnprovenBytes is the regression test for silent
// corruption. Progress is a count of downloaded bytes, not a position, and
// first/last-piece priority makes the file sparse from the start — so a claim
// that "the first 40% is present" is false in the middle, and a read into the
// hole succeeds and returns zeros. The player then decodes nulls as video and
// drifts, with nothing logged.
func TestEstimateNeverClaimsUnprovenBytes(t *testing.T) {
	q := &estimateQBit{progress: 0.4} // 40% downloaded, sparse
	c := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})
	a := newFileAvailability(c, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 8<<20)

	// Well inside the "downloaded" 40% by the old contiguous assumption.
	if a.BytesAvailable(context.Background(), 1<<20, 4096) {
		t.Fatal("an unproven range must never be reported available without piece data")
	}
	// And a range past it, for completeness.
	if a.BytesAvailable(context.Background(), 7<<20, 4096) {
		t.Fatal("a range beyond the estimate must also be refused")
	}
}

// TestEstimateAllowsCompletedFile: once the file is finished every byte really
// is present, so the fallback can serve it.
func TestEstimateAllowsCompletedFile(t *testing.T) {
	q := &estimateQBit{progress: 1}
	c := qbittorrent.New(qbittorrent.Options{BaseURL: q.server(t).URL, Category: "seedstream"})
	a := newFileAvailability(c, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 0, 8<<20)

	if !a.BytesAvailable(context.Background(), 4<<20, 4096) {
		t.Fatal("a completed file must be servable through the fallback")
	}
}

// TestReadUsesVerifiedOffset covers the second half: the bytes returned must be
// the bytes that were checked. A plain Read takes the descriptor's own offset,
// which can drift from the verified position; ReadAt cannot.
func TestReadUsesVerifiedOffset(t *testing.T) {
	const size = 1 << 20
	dir := t.TempDir()
	path := filepath.Join(dir, "v.mkv")
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()

	r := newSeekableFileReader(fh, completeAvail(size), size)
	const target = 4096
	if _, err := r.Seek(target, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}

	// Move the descriptor out from under the reader, as a partial read or a
	// recovery re-seek could.
	if _, err := fh.Seek(999, io.SeekStart); err != nil {
		t.Fatalf("desync: %v", err)
	}

	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if n == 0 {
		t.Fatal("expected data")
	}
	if buf[0] != data[target] {
		t.Fatalf("read from the wrong offset: got %d, want %d (descriptor drift not isolated)",
			buf[0], data[target])
	}
}
