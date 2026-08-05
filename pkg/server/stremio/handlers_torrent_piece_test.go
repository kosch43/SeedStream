package stremio

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/release"
	"seedstream/pkg/session"
	"seedstream/pkg/torrent"
)

// Full play path with a piece-aware qBittorrent: a range request inside the
// downloaded region returns real bytes; the reader is the piece-checked one.
func TestServeTorrentWithPieceData(t *testing.T) {
	const pieceSize = 1 << 20
	const totalPieces = 64
	const fileSize = totalPieces * pieceSize
	saveDir := t.TempDir()
	name := "Movie.2024.1080p.mkv"
	// Write real data for the first 8 pieces (downloaded); rest sparse.
	fp := filepath.Join(saveDir, name)
	f, _ := os.Create(fp)
	f.Truncate(fileSize)
	blk := make([]byte, 65536)
	for i := range blk {
		blk[i] = 0x7E
	}
	for off := int64(0); off < 24*pieceSize; off += int64(len(blk)) {
		f.WriteAt(blk, off)
	}
	f.Close()

	hash := "0123456789abcdef0123456789abcdef01234567"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x"})
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "v5.0.0") })
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "Ok.") })
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"hash":%q,"name":%q,"size":%d,"progress":0.375,"state":"downloading","save_path":%q,"category":"seedstream","added_on":1,"last_activity":2}]`, hash, name, fileSize, saveDir)
	})
	mux.HandleFunc("/api/v2/torrents/files", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"index":0,"name":%q,"size":%d,"progress":0.375,"priority":1,"piece_range":[0,%d]}]`, name, fileSize, totalPieces-1)
	})
	mux.HandleFunc("/api/v2/torrents/properties", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"piece_size":%d,"pieces_num":%d,"total_size":%d}`, pieceSize, totalPieces, fileSize)
	})
	mux.HandleFunc("/api/v2/torrents/pieceStates", func(w http.ResponseWriter, r *http.Request) {
		out := "["
		for i := 0; i < totalPieces; i++ {
			if i > 0 {
				out += ","
			}
			if i < 24 {
				out += "2"
			} else {
				out += "0"
			}
		}
		out += "]"
		w.Write([]byte(out))
	})
	qbit := httptest.NewServer(mux)
	defer qbit.Close()

	cfg := &config.Config{
		AddonBaseURL:   "http://127.0.0.1:7000",
		TorrentClients: []config.TorrentClientConfig{{Name: "seedbox", Type: "qbittorrent", URL: qbit.URL, SavePath: saveDir}},
	}
	sessMgr := session.NewManager(time.Minute)
	defer sessMgr.Shutdown()
	srv := &Server{
		manifest: NewManifest("test"), version: "test", baseURL: cfg.AddonBaseURL,
		config: cfg, sessionManager: sessMgr, uniqueIndexerHits: map[string]int64{},
	}
	srv.torrentManager = torrent.NewManager(cfg.TorrentClients)

	rel := &release.Release{Title: "Movie 2024 1080p", Protocol: "torrent", InfoHash: hash, Magnet: "magnet:?xt=urn:btih:" + hash}
	slot := formatStreamSlotPath("default", "movie", "tt1", 0)
	sessMgr.CreateDeferredSession(slot, rel, &session.ContentMeta{ImdbID: "tt1"}, "movie", "tt1", "Movie", "default")

	// Range fully inside the downloaded region (piece 2).
	req := httptest.NewRequest(http.MethodGet, "/play/"+slot, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 2*pieceSize, 2*pieceSize+999))
	rec := httptest.NewRecorder()
	srv.handlePlay(rec, req, nil)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("downloaded-region range: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 1000 || rec.Body.Bytes()[0] != 0x7E {
		t.Fatalf("downloaded-region range returned wrong data: len=%d first=%x", rec.Body.Len(), rec.Body.Bytes()[0])
	}
}
