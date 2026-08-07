package stremio

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/core/logger"
	"seedstream/pkg/core/paths"
)

// errorVideoPath is where the clip a failed playback redirects to is served
// from. Stremio has no way to show an error message during playback — the
// player is already committed to a video stream — so the only way to tell a
// viewer why nothing is playing is to play them something that says so.
const errorVideoPath = "/error/failure.mp4"

// errorVideoOverrideName is the file an operator can drop into the data
// directory to replace the bundled clip. The data directory is the one place
// that survives a container rebuild, so a replacement put there keeps working
// across upgrades — whereas editing the copy inside the image is undone by the
// next `docker compose build`.
const errorVideoOverrideName = "failure.mp4"

// Accepted alongside the mp4, since re-encoding a clip just to change the
// container is a waste. Served with the matching type so players do not have to
// sniff. Order is preference order.
var errorVideoNames = []string{
	"failure.mp4",
	"failure.mkv",
	"failure.webm",
}

var (
	errorVideoOnce sync.Once
	errorVideoDir  string
)

// errorVideoOverride returns the path of an operator-supplied clip, or "" when
// there is none. Looked up on each request rather than cached, so dropping a
// file in takes effect without a restart; the directory itself is resolved once.
func errorVideoOverride() string {
	errorVideoOnce.Do(func() {
		errorVideoDir = filepath.Join(paths.GetDataDir(), "error")
	})
	if errorVideoDir == "" {
		return ""
	}
	for _, name := range errorVideoNames {
		candidate := filepath.Join(errorVideoDir, name)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() && st.Size() > 0 {
			return candidate
		}
	}
	return ""
}

// serveErrorVideo serves the operator's clip when one exists, and the bundled
// one otherwise.
//
// The response is deliberately uncacheable. A player that cached this would
// keep showing the failure clip for a title that has since started working,
// which reads as SeedStream being broken rather than one attempt having failed.
func serveErrorVideo(w http.ResponseWriter, r *http.Request, bundled http.Handler) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	if custom := errorVideoOverride(); custom != "" {
		f, err := os.Open(custom)
		if err == nil {
			defer f.Close()
			if st, serr := f.Stat(); serr == nil {
				if ctype := errorVideoContentType(custom); ctype != "" {
					w.Header().Set("Content-Type", ctype)
				}
				// ServeContent rather than a copy, so range requests work: a
				// player that seeks in the clip must not receive the whole file
				// with a 200 when it asked for a range.
				http.ServeContent(w, r, filepath.Base(custom), st.ModTime(), f)
				return
			}
		}
		logger.Warn("Could not read the custom failure video, falling back to the bundled one",
			"path", custom, "err", err)
	}

	if bundled == nil {
		http.Error(w, "failure video unavailable", http.StatusNotFound)
		return
	}
	// The bundled clip only exists as an mp4, so the request has to name it
	// whatever extension the override happened to use.
	r.URL.Path = errorVideoPath
	bundled.ServeHTTP(w, r)
}

func errorVideoContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	default:
		return ""
	}
}

// ErrorVideoStatus describes the failure clip for the settings page.
type ErrorVideoStatus struct {
	// Custom is true when an operator-supplied clip is in use.
	Custom bool `json:"custom"`
	// Path is where a replacement is read from, whether or not one is there
	// yet, so the UI can tell the operator where to put it.
	Path string `json:"path"`
	// Names are the filenames accepted at that location, in preference order.
	Names []string `json:"names"`
	// Size and ModTime describe the clip in use, when it is a custom one.
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

// ErrorVideoStatus reports which failure clip is in use and where to put a
// replacement.
func (s *Server) ErrorVideoStatus() ErrorVideoStatus {
	errorVideoOnce.Do(func() {
		errorVideoDir = filepath.Join(paths.GetDataDir(), "error")
	})
	out := ErrorVideoStatus{Path: errorVideoDir, Names: errorVideoNames}
	if custom := errorVideoOverride(); custom != "" {
		out.Custom = true
		out.Path = custom
		if st, err := os.Stat(custom); err == nil {
			out.Size = st.Size()
			out.ModTime = st.ModTime()
		}
	}
	return out
}
