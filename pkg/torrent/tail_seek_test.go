package torrent

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// completeAvail builds a fileAvailability that reports everything downloaded,
// standing in for a finished torrent without needing a qBittorrent server.
func completeAvail(fileSize int64) *fileAvailability {
	return completedAvailability(fileSize)
}

// serveRange runs a Range request through http.ServeContent exactly as the
// playback handler does, and returns the response.
func serveRange(t *testing.T, content io.ReadSeeker, rangeHeader string) *http.Response {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "video.mkv", time.Unix(0, 0), content)
	})
	req := httptest.NewRequest(http.MethodGet, "/play", nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// TestTailSeekOnCompleteFile reproduces the reported bug: seeking close to the
// end of a fully downloaded file ends playback instead of serving the tail.
func TestTailSeekOnCompleteFile(t *testing.T) {
	const fileSize = 8 << 20 // 8 MiB
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")

	// A fully written file: every byte is real data.
	data := make([]byte, fileSize)
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

	r := newSeekableFileReader(fh, completeAvail(fileSize), fileSize, nil)

	// Seek to 90% of the way in, the way a player does when you skip near the end.
	start := int64(fileSize) * 90 / 100
	resp := serveRange(t, r, fmt.Sprintf("bytes=%d-", start))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("tail seek should return 206 Partial Content, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := fileSize - int(start)
	if len(body) != want {
		t.Fatalf("tail seek returned %d bytes, want %d (Content-Range: %q)",
			len(body), want, resp.Header.Get("Content-Range"))
	}
	if body[0] != data[start] {
		t.Fatalf("tail seek returned wrong data: got %d want %d", body[0], data[start])
	}
	if body[len(body)-1] != data[fileSize-1] {
		t.Fatalf("last byte wrong: got %d want %d", body[len(body)-1], data[fileSize-1])
	}
}

// TestTailSeekDoesNotTruncateWhenTailNotWritten is the regression test for the
// reported bug. qBittorrent writes pieces as they arrive, so until the final
// piece lands the file on disk is physically smaller than the torrent says.
// Reading past that point used to return io.EOF from the OS, which ended the
// response after Content-Length had already promised the full range — the player
// saw a seek near the end as the end of the video and glitched out.
//
// The reader must wait for the missing tail instead of reporting EOF, and must
// never deliver fewer bytes than it promised.
func TestTailSeekDoesNotTruncateWhenTailNotWritten(t *testing.T) {
	const logicalSize = 8 << 20
	const physicalSize = 4 << 20 // only half written to disk

	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	data := make([]byte, physicalSize)
	for i := range data {
		data[i] = 0xAB
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()

	// Keep the wait short so the test fails fast rather than blocking.
	prev := seekWaitTimeout
	seekWaitTimeout = 2 * time.Second
	defer func() { seekWaitTimeout = prev }()

	r := newSeekableFileReader(fh, completeAvail(logicalSize), logicalSize, nil)
	r.Seek(int64(logicalSize)*90/100, io.SeekStart)

	buf := make([]byte, 32*1024)
	start := time.Now()
	n, err := r.Read(buf)
	elapsed := time.Since(start)

	if err == io.EOF {
		t.Fatal("reading an unwritten tail must not report EOF — that truncates the response and ends playback")
	}
	if n > 0 {
		t.Fatalf("no data should have been produced from an unwritten region, got %d bytes", n)
	}
	if elapsed < time.Second {
		t.Fatalf("returned after %v — it should have waited for the tail to arrive", elapsed)
	}
}

// TestReadReportsGenuineEOFAtLogicalEnd: once the logical end is reached the
// stream really is over, and playback must be allowed to finish normally.
func TestReadReportsGenuineEOFAtLogicalEnd(t *testing.T) {
	const fileSize = 1 << 20
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mkv")
	if err := os.WriteFile(path, make([]byte, fileSize), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fh, _ := os.Open(path)
	defer fh.Close()

	r := newSeekableFileReader(fh, completeAvail(fileSize), fileSize, nil)
	r.Seek(0, io.SeekEnd)

	if _, err := r.Read(make([]byte, 4096)); err != io.EOF {
		t.Fatalf("at the logical end the reader must return io.EOF, got %v", err)
	}
}
