// Package torrent wires torrent releases to a seedbox qBittorrent for playback.
//
// SeedStream does not download or seed torrents itself. When a torrent release
// is played it is handed to a configured qBittorrent (running on a seedbox),
// which downloads sequentially so the head of the file is ready quickly and
// then keeps seeding indefinitely for private-tracker ratio. SeedStream reads
// the finished/partial file from the client's save path and serves it with HTTP
// range support, so the same download both streams and seeds.
package torrent

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/release"
	"seedstream/pkg/torrent/qbittorrent"
)

// DefaultBufferBytes is how much of the target file's head must be on disk
// before playback starts. Sequential download keeps the rest ahead of the player.
const DefaultBufferBytes int64 = 16 * 1024 * 1024

// DefaultPrepareTimeout bounds how long PrepareForPlayback waits for the buffer.
const DefaultPrepareTimeout = 90 * time.Second

var videoExts = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".avi": {}, ".m4v": {}, ".mov": {},
	".wmv": {}, ".flv": {}, ".webm": {}, ".ts": {}, ".m2ts": {},
}

// Manager owns the configured torrent clients.
type Manager struct {
	clients []*clientEntry
}

type clientEntry struct {
	cfg    config.TorrentClientConfig
	client *qbittorrent.Client
}

// NewManager builds a Manager from config. Disabled or qBittorrent-less entries
// are skipped. Returns a Manager that reports Enabled()==false when none apply.
func NewManager(clients []config.TorrentClientConfig) *Manager {
	m := &Manager{}
	for _, c := range clients {
		if !c.IsEnabled() {
			continue
		}
		if strings.ToLower(strings.TrimSpace(c.Type)) != "qbittorrent" {
			continue
		}
		if strings.TrimSpace(c.URL) == "" {
			continue
		}
		m.clients = append(m.clients, &clientEntry{
			cfg: c,
			client: qbittorrent.New(qbittorrent.Options{
				BaseURL:  c.URL,
				Username: c.Username,
				Password: c.Password,
				Category: c.CategoryOrDefault(),
				SavePath: c.SavePath,
			}),
		})
	}
	return m
}

// Enabled reports whether any torrent client is configured.
func (m *Manager) Enabled() bool { return m != nil && len(m.clients) > 0 }

// TorrentHealthEntry is a flattened view of one qBittorrent torrent used by
// the Cerberus watchdog for stall detection.
type TorrentHealthEntry struct {
	ClientName   string
	Hash         string
	Name         string
	State        string
	Progress     float64
	LastActivity time.Time
	AddedAt      time.Time
}

// ListAll returns all SeedStream-category torrents across every configured
// client. Errors from individual clients are logged and skipped.
func (m *Manager) ListAll(ctx context.Context) ([]TorrentHealthEntry, error) {
	var out []TorrentHealthEntry
	for _, e := range m.clients {
		list, err := e.client.ListCategory(ctx)
		if err != nil {
			logger.Warn("Cerberus ListAll: ListCategory failed", "client", e.cfg.Name, "err", err)
			continue
		}
		for _, t := range list {
			out = append(out, TorrentHealthEntry{
				ClientName:   e.cfg.Name,
				Hash:         t.Hash,
				Name:         t.Name,
				State:        t.State,
				Progress:     t.Progress,
				LastActivity: time.Unix(t.LastActivity, 0),
				AddedAt:      time.Unix(t.AddedOn, 0),
			})
		}
	}
	return out, nil
}

// Replace removes a stalled torrent (without deleting files) and adds a
// replacement magnet/URL to the same client.
func (m *Manager) Replace(ctx context.Context, clientName, oldHash, newURL string) error {
	var c *qbittorrent.Client
	for _, e := range m.clients {
		if e.cfg.Name == clientName {
			c = e.client
			break
		}
	}
	if c == nil {
		return fmt.Errorf("torrent client %q not found", clientName)
	}
	if err := c.Delete(ctx, []string{oldHash}, false); err != nil {
		return fmt.Errorf("delete stalled torrent: %w", err)
	}
	return c.Add(ctx, qbittorrent.AddOptions{URL: newURL, Sequential: true})
}

// Ping checks each configured client; returns a map of name -> error (nil = ok).
func (m *Manager) Ping(ctx context.Context) map[string]error {
	out := make(map[string]error, len(m.clients))
	for _, e := range m.clients {
		out[e.cfg.Name] = e.client.Ping(ctx)
	}
	return out
}

// PrepareResult describes a torrent file ready (or buffering) for playback.
type PrepareResult struct {
	// AbsPath is the absolute path on the local filesystem (the seedbox save
	// path, which SeedStream can read) of the chosen video file.
	AbsPath string
	Name    string
	Size    int64
}

// PrepareForPlayback ensures the release's torrent is present in a seedbox
// qBittorrent (adding it if needed), waits for the chosen file's head to buffer,
// and returns the file's absolute path for range serving. The torrent is left
// running so it keeps seeding.
//
// clientOverride, if non-nil, is used instead of the global client list. This
// lets each stream route to its own seedbox qBittorrent.
func (m *Manager) PrepareForPlayback(ctx context.Context, rel *release.Release, season, episode int, bufferBytes int64, timeout time.Duration, clientOverride *config.TorrentClientConfig) (*PrepareResult, error) {
	if rel == nil || !rel.IsTorrent() {
		return nil, fmt.Errorf("release is not a torrent")
	}
	addURL := rel.Magnet
	if addURL == "" {
		addURL = rel.Link
	}
	if addURL == "" && rel.InfoHash == "" {
		return nil, fmt.Errorf("torrent release has no magnet, link, or infohash")
	}
	if bufferBytes <= 0 {
		bufferBytes = DefaultBufferBytes
	}
	if timeout <= 0 {
		timeout = DefaultPrepareTimeout
	}

	// Resolve which qBittorrent client to use: prefer the stream-level override
	// so each member can point to their own seedbox, fall back to the first
	// globally configured client.
	var c *qbittorrent.Client
	if clientOverride != nil && strings.TrimSpace(clientOverride.URL) != "" {
		c = qbittorrent.New(qbittorrent.Options{
			BaseURL:  clientOverride.URL,
			Username: clientOverride.Username,
			Password: clientOverride.Password,
			Category: clientOverride.CategoryOrDefault(),
			SavePath: clientOverride.SavePath,
		})
	} else {
		if !m.Enabled() {
			return nil, fmt.Errorf("no torrent client configured")
		}
		c = m.clients[0].client
	}

	hash := strings.ToLower(strings.TrimSpace(rel.InfoHash))

	// Reuse an existing download if we can identify it by hash.
	var info *qbittorrent.TorrentInfo
	if hash != "" {
		if existing, err := c.Get(ctx, hash); err == nil && existing != nil {
			info = existing
		}
	}

	if info == nil {
		if addURL == "" {
			return nil, fmt.Errorf("torrent not present and no add URL available")
		}
		if err := c.Add(ctx, qbittorrent.AddOptions{URL: addURL, Sequential: true}); err != nil {
			return nil, fmt.Errorf("add torrent: %w", err)
		}
		var err error
		info, err = m.resolveAdded(ctx, c, hash)
		if err != nil {
			return nil, err
		}
	}

	deadline := time.Now().Add(timeout)
	for {
		files, err := c.Files(ctx, info.Hash)
		if err == nil && len(files) > 0 {
			f := pickVideoFile(files, season, episode)
			if f != nil {
				done := int64(f.Progress * float64(f.Size))
				if f.Progress >= 0.999 || done >= bufferBytes {
					abs := absFilePath(c.SavePath(), info, f.Name)
					return &PrepareResult{AbsPath: abs, Name: filepath.Base(f.Name), Size: f.Size}, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("torrent still buffering after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// resolveAdded finds the torrent just added. Prefers the known hash; otherwise
// takes the most recently added torrent in the category.
func (m *Manager) resolveAdded(ctx context.Context, c *qbittorrent.Client, hash string) (*qbittorrent.TorrentInfo, error) {
	for i := 0; i < 12; i++ {
		if hash != "" {
			if info, err := c.Get(ctx, hash); err == nil && info != nil {
				return info, nil
			}
		} else {
			if list, err := c.ListCategory(ctx); err == nil && len(list) > 0 {
				newest := &list[0]
				for j := range list {
					if list[j].AddedOn > newest.AddedOn {
						newest = &list[j]
					}
				}
				return newest, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("could not resolve torrent after add")
}

var seasonEpisodeRe = regexp.MustCompile(`(?i)s(\d{1,2})[ ._-]*e(\d{1,3})`)

// pickVideoFile chooses the file to play. For a series request it prefers a file
// whose name matches SxxExx; otherwise it falls back to the largest video file.
func pickVideoFile(files []qbittorrent.FileInfo, season, episode int) *qbittorrent.FileInfo {
	var largest *qbittorrent.FileInfo
	for i := range files {
		if !isVideo(files[i].Name) {
			continue
		}
		if largest == nil || files[i].Size > largest.Size {
			largest = &files[i]
		}
	}
	if season > 0 && episode > 0 {
		for i := range files {
			if !isVideo(files[i].Name) {
				continue
			}
			if mm := seasonEpisodeRe.FindStringSubmatch(files[i].Name); mm != nil {
				if atoi(mm[1]) == season && atoi(mm[2]) == episode {
					return &files[i]
				}
			}
		}
	}
	return largest
}

func isVideo(name string) bool {
	_, ok := videoExts[strings.ToLower(filepath.Ext(name))]
	return ok
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// absFilePath resolves the on-disk path of a torrent file. qBittorrent's file
// Name is relative to the torrent's save path; content_path/save_path from the
// client are preferred when present so SeedStream reads exactly where the
// seedbox wrote the data.
func absFilePath(configuredSavePath string, info *qbittorrent.TorrentInfo, fileName string) string {
	base := strings.TrimSpace(info.SavePath)
	if base == "" {
		base = strings.TrimSpace(configuredSavePath)
	}
	if base == "" {
		logger.Debug("torrent file path: no save path known, using relative name", "file", fileName)
		return fileName
	}
	return filepath.Join(base, fileName)
}
