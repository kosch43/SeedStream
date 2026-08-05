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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

	aggMu      sync.Mutex
	aggCached  AggregateStats
	aggExpires time.Time
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

// Reannounce asks qBittorrent to re-contact all trackers for a torrent,
// which can revive a stalled download when new peers join the swarm.
// Used instead of Replace when the torrent has partial progress to avoid
// private-tracker H&R violations.
func (m *Manager) Reannounce(ctx context.Context, clientName, hash string) error {
	for _, e := range m.clients {
		if e.cfg.Name == clientName {
			return e.client.Reannounce(ctx, hash)
		}
	}
	return fmt.Errorf("torrent client %q not found", clientName)
}

// AddTorrent adds a magnet/URL to the named client. Used to restore a torrent
// after a failed Replace so content is not silently lost from qBittorrent.
func (m *Manager) AddTorrent(ctx context.Context, clientName, addURL string) error {
	for _, e := range m.clients {
		if e.cfg.Name == clientName {
			return e.client.Add(ctx, qbittorrent.AddOptions{URL: addURL, Sequential: true})
		}
	}
	return fmt.Errorf("torrent client %q not found", clientName)
}

// Replace removes a zero-progress stalled torrent (without deleting files)
// and adds a replacement magnet/URL to the same client. Callers must verify
// Progress == 0 before calling to avoid private-tracker H&R violations.
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

// Resume starts a paused/stopped torrent on the named client. Used by the
// watchdog to get a completed-but-paused torrent seeding again (H&R safety) or
// to un-pause a download that was paused mid-flight.
func (m *Manager) Resume(ctx context.Context, clientName, hash string) error {
	c := m.clientByName(clientName)
	if c == nil {
		return fmt.Errorf("torrent client %q not found", clientName)
	}
	return c.Resume(ctx, hash)
}

// Ping checks each configured client; returns a map of name -> error (nil = ok).
func (m *Manager) Ping(ctx context.Context) map[string]error {
	out := make(map[string]error, len(m.clients))
	for _, e := range m.clients {
		out[e.cfg.Name] = e.client.Ping(ctx)
	}
	return out
}

// HnRRules describes a tracker's Hit-and-Run obligation.
type HnRRules struct {
	MinSeedHours float64
	MinRatio     float64
	// Mode is "any" (seed time OR ratio satisfies, default) or "all" (both required).
	Mode string
}

// Satisfied returns true when the seeding obligation described by these rules
// has been met. Always returns true when no thresholds are configured.
func (r *HnRRules) Satisfied(seedingHours, ratio float64) bool {
	if r == nil || (r.MinSeedHours <= 0 && r.MinRatio <= 0) {
		return true
	}
	seedOK := r.MinSeedHours <= 0 || seedingHours >= r.MinSeedHours
	ratioOK := r.MinRatio <= 0 || ratio >= r.MinRatio
	if strings.EqualFold(strings.TrimSpace(r.Mode), "all") {
		return seedOK && ratioOK
	}
	return seedOK || ratioOK
}

// SeedingStatus holds the current seeding metrics for a torrent.
type SeedingStatus struct {
	SeedingHours float64
	Ratio        float64
	Uploaded     int64
}

// GetSeedingStatus queries qBittorrent for the current seeding time and ratio
// of a torrent. Returns an error if the client is not found or the torrent is
// not present in qBittorrent.
func (m *Manager) GetSeedingStatus(ctx context.Context, hash, clientName string) (*SeedingStatus, error) {
	c := m.clientByName(clientName)
	if c == nil {
		return nil, fmt.Errorf("torrent client %q not found", clientName)
	}
	info, err := c.Get(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent get: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("torrent %s not found in qBittorrent", hash)
	}
	return &SeedingStatus{
		SeedingHours: float64(info.SeedingTime) / 3600,
		Ratio:        info.Ratio,
		Uploaded:     info.Uploaded,
	}, nil
}

// clientByName finds the managed qBittorrent client with the given name.
func (m *Manager) clientByName(name string) *qbittorrent.Client {
	for _, e := range m.clients {
		if e.cfg.Name == name {
			return e.client
		}
	}
	return nil
}

// OpenForPlayback opens the torrent file described by res for HTTP range
// serving. If the torrent is still downloading, it returns a
// SeekableFileReader that waits for each requested byte range to be on disk
// before reading, so that seeking forward into un-downloaded regions blocks
// instead of returning zeros or a 416 error.
//
// Only a torrent qBittorrent reports as fully complete (progress == 1) gets a
// bare *os.File with no checking. A file that merely looked "nearly done" at
// prepare time (progress 0.999) is still wrapped, because 0.1% of a 30 GB file
// is ~30 MB that may not be on disk — the availability checker is cheap when
// the file is in fact complete (it latches after one confirming call).
func (m *Manager) OpenForPlayback(res *PrepareResult) (io.ReadSeekCloser, error) {
	f, err := os.Open(res.AbsPath)
	if err != nil {
		return nil, err
	}
	if res.Progress >= 1 || res.Hash == "" {
		return f, nil
	}
	c := m.clientByName(res.ClientName)
	if c == nil {
		// Client not found (override path) — fall back to plain file.
		return f, nil
	}
	avail := newFileAvailability(c, res.Hash, res.FileIndex, res.Size)
	return newSeekableFileReader(f, avail, res.Size), nil
}

// PrepareResult describes a torrent file ready (or buffering) for playback.
type PrepareResult struct {
	// AbsPath is the absolute path on the local filesystem (the seedbox save
	// path, which SeedStream can read) of the chosen video file.
	AbsPath    string
	Name       string
	Size       int64
	Hash       string  // torrent infohash for progress polling
	FileIndex  int     // file index within the torrent
	ClientName string  // name of the qBittorrent client that holds this torrent
	Progress   float64 // download progress at prepare time (0..1)
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
	var clientName, remotePath string
	if clientOverride != nil && strings.TrimSpace(clientOverride.URL) != "" {
		c = qbittorrent.New(qbittorrent.Options{
			BaseURL:  clientOverride.URL,
			Username: clientOverride.Username,
			Password: clientOverride.Password,
			Category: clientOverride.CategoryOrDefault(),
			SavePath: clientOverride.SavePath,
		})
		clientName = strings.TrimSpace(clientOverride.Name)
		remotePath = strings.TrimSpace(clientOverride.RemotePath)
	} else {
		if !m.Enabled() {
			return nil, fmt.Errorf("no torrent client configured")
		}
		c = m.clients[0].client
		clientName = m.clients[0].cfg.Name
		remotePath = m.clients[0].cfg.RemotePath
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
	prioritySet := false
	for {
		files, err := c.Files(ctx, info.Hash)
		if err == nil && len(files) > 0 {
			f := pickVideoFile(files, season, episode)
			if f != nil {
				// On a multi-file torrent (e.g. a season pack), pull the file
				// being played ahead of the rest so its head buffers first.
				// Priority 7 (maximum) only reorders downloads; it never
				// disables the other files, so the torrent still completes and
				// private-tracker seeding obligations stay intact. Best-effort:
				// a failure here just falls back to sequential order.
				if !prioritySet && len(files) > 1 {
					prioritySet = true
					if err := c.SetFilePriority(ctx, info.Hash, f.Index, 7); err != nil {
						logger.Debug("torrent prepare: set file priority failed", "hash", info.Hash, "file", f.Index, "err", err)
					}
				}
				done := int64(f.Progress * float64(f.Size))
				if f.Progress >= 0.999 || done >= bufferBytes {
					abs := absFilePath(remotePath, c.SavePath(), info, f.Name)
					return &PrepareResult{
						AbsPath:    abs,
						Name:       filepath.Base(f.Name),
						Size:       f.Size,
						Hash:       info.Hash,
						FileIndex:  f.Index,
						ClientName: clientName,
						Progress:   f.Progress,
					}, nil
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

// absFilePath resolves the on-disk path of a torrent file.
//
// remotePath and localSavePath implement remote-seedbox support:
//   - remotePath: qBittorrent's save path on its own machine (e.g. /downloads).
//     Only needed when qBit and SeedStream are on different hosts.
//   - localSavePath: the path SeedStream reads from (e.g. /mnt/seedbox on a
//     remote setup, or the same as qBit's path on a single-machine setup).
//
// When remotePath is set, this function strips it from the full qBittorrent
// path and replaces it with localSavePath, translating between the two
// namespaces. When remotePath is empty, the qBittorrent-reported path is used
// as-is (same-machine case), falling back to localSavePath if qBit didn't
// return one.
func absFilePath(remotePath, localSavePath string, info *qbittorrent.TorrentInfo, fileName string) string {
	qbitBase := strings.TrimSpace(info.SavePath)
	if qbitBase == "" {
		// qBit didn't report its save path; use remotePath if set, else localSavePath.
		if r := strings.TrimSpace(remotePath); r != "" {
			qbitBase = r
		} else {
			qbitBase = strings.TrimSpace(localSavePath)
		}
	}
	if qbitBase == "" {
		logger.Debug("torrent file path: no save path known, using relative name", "file", fileName)
		return fileName
	}

	full := filepath.Join(qbitBase, fileName)

	// Path translation for remote seedbox: strip the remote prefix and replace
	// with the local mount point so SeedStream can open the file.
	remotePfx := filepath.Clean(strings.TrimSpace(remotePath))
	localBase := strings.TrimSpace(localSavePath)
	if remotePfx != "" && remotePfx != "." && localBase != "" {
		sep := string(filepath.Separator)
		if strings.HasPrefix(full, remotePfx+sep) || full == remotePfx {
			rel := strings.TrimPrefix(full, remotePfx)
			return filepath.Join(localBase, rel)
		}
	}

	return full
}

// ClientStats is live activity for a single torrent client. The dashboard
// renders one of these per configured seedbox, so figures shown on a client's
// card belong to that client rather than to the fleet.
type ClientStats struct {
	Name             string
	DownloadSpeedBps int64
	UploadSpeedBps   int64
	ActiveTorrents   int
	TotalTorrents    int
	Seeds            int
	Peers            int
	// Reachable is false when this client's API call failed, so the UI can say
	// "unreachable" instead of silently showing zeros that look like idleness.
	Reachable bool
}

// AggregateStats summarises live activity across every configured torrent
// client, for the dashboard. Speeds are bytes/second as reported by
// qBittorrent; Downloaded/Uploaded are cumulative totals for the SeedStream
// category. Clients carries the same figures broken down per client.
type AggregateStats struct {
	DownloadSpeedBps int64
	UploadSpeedBps   int64
	ActiveTorrents   int // currently moving data (up or down)
	TotalTorrents    int
	DownloadedBytes  int64
	UploadedBytes    int64
	Seeds            int
	Peers            int
	Clients          []ClientStats
}

// aggregateCacheTTL bounds how often the dashboard poll actually reaches
// qBittorrent. The websocket pushes stats every second per connected client,
// which would otherwise hammer the seedbox WebUI.
const aggregateCacheTTL = 2 * time.Second

// AggregateStats returns live totals across all clients, cached briefly.
// Client errors are skipped so one unreachable seedbox does not blank the
// dashboard for the others.
func (m *Manager) AggregateStats(ctx context.Context) AggregateStats {
	if m == nil || len(m.clients) == 0 {
		return AggregateStats{}
	}

	// The lock is deliberately held across the client calls. Releasing it to
	// fetch would let every concurrent caller miss the cache at once and hit the
	// seedbox in parallel, which is the load this cache exists to prevent. Held
	// this way, the first caller refreshes and the rest queue briefly and then
	// read the fresh value.
	m.aggMu.Lock()
	defer m.aggMu.Unlock()
	if time.Now().Before(m.aggExpires) {
		return m.aggCached
	}

	out := AggregateStats{Clients: make([]ClientStats, 0, len(m.clients))}
	for _, e := range m.clients {
		cs := ClientStats{Name: e.cfg.Name}
		list, err := e.client.ListCategory(ctx)
		if err != nil {
			logger.Debug("torrent aggregate stats: list failed", "client", e.cfg.Name, "err", err)
			out.Clients = append(out.Clients, cs) // Reachable stays false
			continue
		}
		cs.Reachable = true
		for _, t := range list {
			cs.TotalTorrents++
			cs.DownloadSpeedBps += t.DlSpeed
			cs.UploadSpeedBps += t.UpSpeed
			cs.Seeds += t.NumSeeds
			cs.Peers += t.NumLeechs
			if t.DlSpeed > 0 || t.UpSpeed > 0 {
				cs.ActiveTorrents++
			}
			out.DownloadedBytes += t.Downloaded
			out.UploadedBytes += t.Uploaded
		}
		out.DownloadSpeedBps += cs.DownloadSpeedBps
		out.UploadSpeedBps += cs.UploadSpeedBps
		out.ActiveTorrents += cs.ActiveTorrents
		out.TotalTorrents += cs.TotalTorrents
		out.Seeds += cs.Seeds
		out.Peers += cs.Peers
		out.Clients = append(out.Clients, cs)
	}

	m.aggCached = out
	m.aggExpires = time.Now().Add(aggregateCacheTTL)
	return out
}
