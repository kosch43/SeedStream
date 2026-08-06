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

// StreamableHeadFraction is the ceiling on the head buffer, as a fraction of
// the file. A tenth of a film is minutes of video — far more headroom than
// playback needs — so once that much is on disk continuously from the start,
// the stream plays, whatever a bitrate estimate asked for.
//
// It exists because every other input to the buffer size is an estimate:
// runtime comes from metadata that is sometimes wrong, and bitrate is derived
// from it. A bad estimate should be able to delay a stream by seconds, never
// hold it back past the point where the answer is obviously yes.
const StreamableHeadFraction = 0.10

// requiredHeadBytes is how much continuous data must sit at the front of the
// file before playback starts: the requested buffer, capped at a tenth of the
// file, and never more than the file itself.
func requiredHeadBytes(want, fileSize int64) int64 {
	if fileSize <= 0 {
		return want
	}
	if ceiling := int64(float64(fileSize) * StreamableHeadFraction); want > ceiling && ceiling > 0 {
		want = ceiling
	}
	if want > fileSize {
		want = fileSize
	}
	return want
}

// seedCheckGrace is how long a newly added torrent is given to find peers before
// its swarm size is judged. Announcing to trackers and connecting to peers takes
// a few seconds, so an immediate check would reject every torrent.
const seedCheckGrace = 20 * time.Second

var videoExts = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".avi": {}, ".m4v": {}, ".mov": {},
	".wmv": {}, ".flv": {}, ".webm": {}, ".ts": {}, ".m2ts": {},
}

// Manager owns the configured torrent clients.
type Manager struct {
	clients []*clientEntry

	// BufferBytes / PrepareTimeout are the defaults used by PrepareForPlayback
	// when the caller doesn't pass explicit values. Overridable via config.
	BufferBytes    int64
	PrepareTimeout time.Duration
	// MinSeeders is the live swarm size a torrent must reach while buffering.
	// A torrent that is under-seeded and making no progress is abandoned early
	// so playback fails over instead of waiting out the whole timeout. 0 disables.
	MinSeeders int

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
	m := &Manager{
		BufferBytes:    DefaultBufferBytes,
		PrepareTimeout: DefaultPrepareTimeout,
	}
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
	// NumSeeds is the live swarm size qBittorrent is connected to. The watchdog
	// uses it to spot a torrent that is too thinly seeded to finish, which
	// otherwise looks identical to one that is merely slow.
	NumSeeds int
	// CompletedAt is when the download finished, which is when most trackers
	// start counting a seeding obligation. Zero while still downloading.
	CompletedAt time.Time
	// SeedingHours and Ratio are qBittorrent's own accounting of the obligation
	// so far. They are the client's view, not the tracker's.
	SeedingHours float64
	Ratio        float64
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
				NumSeeds:     t.NumSeeds,
				CompletedAt:  time.Unix(t.CompletionOn, 0),
				SeedingHours: float64(t.SeedingTime) / 3600,
				Ratio:        t.Ratio,
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

// Replace adds a healthier alternative alongside a stalled torrent. It does not
// remove the stalled one.
//
// Nothing is deleted, ever. Even a torrent showing zero progress may have been
// announced to a private tracker, and removing it is the one action that cannot
// be undone if the tracker's accounting disagrees with qBittorrent's. Leaving it
// in place costs an idle entry; removing it can cost a hit-and-run. The caller
// marks the old torrent as handled so it is not repeatedly replaced.
func (m *Manager) Replace(ctx context.Context, clientName, oldHash, newURL string) error {
	c := m.clientByName(clientName)
	if c == nil {
		return fmt.Errorf("torrent client %q not found", clientName)
	}
	if err := c.Add(ctx, qbittorrent.AddOptions{URL: newURL, Sequential: true}); err != nil {
		return fmt.Errorf("add replacement torrent: %w", err)
	}
	logger.Info("Torrent replacement added alongside the stalled one (nothing deleted)",
		"client", clientName, "stalled_hash", oldHash)
	return nil
}

// ProtectSeeding pins a torrent's share limits so qBittorrent never stops
// seeding it on its own. SeedStream's model is that a seedbox keeps seeding
// indefinitely for ratio and hit-and-run compliance, which the client's global
// ratio and seed-time limits would otherwise override.
//
// Best-effort: a failure is logged and ignored, since it must never block
// playback, and older clients may not accept every parameter.
func (m *Manager) ProtectSeeding(ctx context.Context, clientName, hash string) {
	c := m.clientByName(clientName)
	if c == nil {
		return
	}
	err := c.SetShareLimits(ctx, hash,
		qbittorrent.NoShareLimit, qbittorrent.NoShareLimit, qbittorrent.NoShareLimit)
	if err != nil {
		logger.Debug("could not pin share limits; qBittorrent's global limits still apply",
			"client", clientName, "hash", shortHash(hash), "err", err)
		return
	}
	logger.Debug("share limits pinned: qBittorrent will not stop seeding this torrent",
		"client", clientName, "hash", shortHash(hash))
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

// SeedingUploadTotals returns each configured client's current session upload
// counter (qBittorrent up_info_data), keyed by client name. Used by the upload
// guard to fold BitTorrent seeding into the monthly total. Unreachable clients
// are skipped rather than reported as an error.
func (m *Manager) SeedingUploadTotals(ctx context.Context) map[string]int64 {
	out := make(map[string]int64, len(m.clients))
	for _, e := range m.clients {
		info, err := e.client.TransferInfo(ctx)
		if err != nil || info == nil {
			logger.Debug("upload guard: transfer info query failed", "client", e.cfg.Name, "err", err)
			continue
		}
		out[e.cfg.Name] = info.UpInfoData
	}
	return out
}

// TorrentPresence describes a torrent that is currently present in one of the
// configured clients.
type TorrentPresence struct {
	Hash       string
	ClientName string
	Progress   float64
	State      string
}

// Complete reports whether the torrent has finished downloading, meaning
// playback can start immediately with no buffering wait.
func (p *TorrentPresence) Complete() bool { return p != nil && p.Progress >= 0.999 }

// FindTorrent locates a torrent by info hash across every configured client and
// reports whether it is still there. This is how SeedStream confirms that a
// torrent it remembers downloading is genuinely available before replaying from
// it — the data may have been deleted on the seedbox since.
// Returns nil when no client holds the torrent.
func (m *Manager) FindTorrent(ctx context.Context, hash string) *TorrentPresence {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if m == nil || hash == "" {
		return nil
	}
	for _, e := range m.clients {
		info, err := e.client.Get(ctx, hash)
		if err != nil || info == nil {
			continue
		}
		return &TorrentPresence{
			Hash:       info.Hash,
			ClientName: e.cfg.Name,
			Progress:   info.Progress,
			State:      info.State,
		}
	}
	return nil
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
// A file that merely looked "nearly done" at prepare time (progress 0.999) is
// still checked, because 0.1% of a 30 GB file is ~30 MB that may not be on disk
// — the availability checker is cheap when the file is in fact complete (it
// latches after one confirming call).
//
// ph, when non-nil, receives the position of every byte served, so the rest of
// SeedStream can see where in the title the viewer is. A completed torrent is
// wrapped too, with a checker that answers from its latch and never touches the
// network: position tracking should not depend on whether the download happened
// to finish.
func (m *Manager) OpenForPlayback(res *PrepareResult, ph *Playhead) (io.ReadSeekCloser, error) {
	f, err := os.Open(res.AbsPath)
	if err != nil {
		return nil, err
	}
	var avail *fileAvailability
	if c := m.clientByName(res.ClientName); res.Progress < 1 && res.Hash != "" && c != nil {
		avail = newFileAvailability(c, res.Hash, res.FileIndex, res.Size)
	} else {
		// Complete, or no client to ask: every byte is on disk, or nothing can
		// be proven about which are. Either way there is nothing to wait for.
		avail = completedAvailability(res.Size)
	}
	return newSeekableFileReader(f, avail, res.Size, ph), nil
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
		bufferBytes = m.BufferBytes
	}
	if timeout <= 0 {
		timeout = m.PrepareTimeout
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
		// Snapshot the category before adding. When the release carries no info
		// hash this is the only reliable way to tell which torrent is ours: the
		// one that appeared after the add. Picking the newest torrent in the
		// category instead would hand back whatever another concurrent request
		// happened to add a moment earlier — a different title entirely.
		before := m.categoryHashes(ctx, c)
		if err := c.Add(ctx, qbittorrent.AddOptions{URL: addURL, Sequential: true}); err != nil {
			return nil, fmt.Errorf("add torrent: %w", err)
		}
		var err error
		info, err = m.resolveAdded(ctx, c, hash, before, rel.Title)
		if err != nil {
			return nil, err
		}
		// Stop qBittorrent's global ratio and seed-time limits from ending this
		// torrent's seeding on their own, which on a private tracker could cut a
		// hit-and-run obligation short. Best-effort; never blocks playback.
		if serr := c.SetShareLimits(ctx, info.Hash,
			qbittorrent.NoShareLimit, qbittorrent.NoShareLimit, qbittorrent.NoShareLimit); serr != nil {
			logger.Debug("could not pin share limits on the added torrent",
				"hash", shortHash(info.Hash), "err", serr)
		}
	}

	// Whatever route this torrent took into qBittorrent, make it download from
	// the front. A torrent the client already held ignores the streaming flags
	// on the add that "created" it, so without this a re-watch — or anything the
	// user grabbed by hand — fills in rarest-first and never presents a
	// continuous head, no matter how much of it is downloaded. Best-effort: a
	// failure here costs ordering, not playback.
	if err := c.EnsureStreamingOrder(ctx, info); err != nil {
		logger.Debug("could not enable sequential download for streaming",
			"hash", shortHash(info.Hash), "err", err)
	}

	deadline := time.Now().Add(timeout)
	prioritySet := false
	// Track why the loop is still waiting so a timeout reports the real reason
	// instead of blaming buffering for what is actually a client failure.
	var lastClientErr error
	clientErrs := 0
	lastProgress := -1.0
	// Readiness is decided on the piece bitmap, not on a byte count. Created
	// once the video file is known and reused across polls so its cached bitmap
	// and complete-latch survive the loop.
	var avail *fileAvailability
	warnedFragmented := false
	fragmentedHead := false
	// The head actually required, once the file size is known and the ceiling
	// has been applied. Kept out here so a timeout reports the real target.
	needHead := bufferBytes
	// Swarm health: a freshly added torrent needs a moment to find peers, so the
	// live seeder count is only judged after a grace period, and only when the
	// download has also failed to advance — a small but fast swarm is fine.
	started := time.Now()
	graceProgress := -1.0
	for {
		files, err := c.Files(ctx, info.Hash)
		if err != nil {
			// The download client itself is failing (auth, network, wrong host).
			// Waiting cannot fix that, and spinning here for the whole timeout is
			// what pushes a play request past an upstream proxy's limit. Give it a
			// couple of retries for a transient blip, then surface the real error.
			lastClientErr = err
			clientErrs++
			if clientErrs >= 3 {
				return nil, fmt.Errorf("torrent client unreachable while preparing playback: %w", err)
			}
		} else if len(files) > 0 {
			clientErrs = 0
			if pickVideoFile(files, season, episode) == nil {
				// The torrent's file list is known and contains no playable video.
				// No amount of waiting will add one.
				return nil, fmt.Errorf("torrent contains no playable video file (%d files)", len(files))
			}
		}
		if err == nil && len(files) > 0 {
			f := pickVideoFile(files, season, episode)
			if f != nil {
				lastProgress = f.Progress
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
				// A byte count cannot say where the bytes are. qBittorrent's
				// sequential download is a preference, not a guarantee: pieces
				// still arrive out of order from whichever peers have them, and
				// first/last-piece priority deliberately pulls the tail in early.
				// So the file can be 84% downloaded with only a handful of
				// contiguous pieces at the front, and a head measured as
				// "progress * size >= bufferBytes" is then mostly holes. Playback
				// starts, drains the few real pieces, and stalls seconds in.
				//
				// Ask the piece bitmap instead: are the first bufferBytes of the
				// file actually on disk, end to end? That is the same question the
				// reader will ask on its first read, so answering it here means
				// prepare returns only when playback can genuinely begin.
				if avail == nil && f.Size > 0 {
					avail = newFileAvailability(c, info.Hash, f.Index, f.Size)
				}
				needHead = requiredHeadBytes(bufferBytes, f.Size)
				headReady := false
				if avail != nil {
					headReady = avail.BytesAvailable(ctx, 0, needHead)
				}
				// Surface the divergence once: the old byte-count test passing
				// while the head is fragmented is precisely the case that used to
				// hand the player an unplayable file.
				if !headReady && f.Size > 0 {
					if done := int64(f.Progress * float64(f.Size)); done >= needHead {
						fragmentedHead = true
						if !warnedFragmented {
							warnedFragmented = true
							logger.Info("Playback: enough of this file is downloaded, but not at the start — waiting for a continuous head instead of starting into holes",
								"hash", shortHash(info.Hash), "file", f.Index,
								"progress", f.Progress, "need_head_bytes", needHead,
								"sequential", info.SequentialDL)
						}
					}
				}
				if headReady {
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
		// Abandon a torrent whose swarm is too thin to ever finish buffering.
		// Waiting out the full timeout on a dead swarm is what makes playback
		// look like it hangs; failing here lets the caller fail over at once.
		if m.MinSeeders > 0 && lastProgress >= 0 && time.Since(started) > seedCheckGrace {
			if graceProgress < 0 {
				graceProgress = lastProgress
			} else if info2, err := c.Get(ctx, info.Hash); err == nil && info2 != nil {
				if info2.NumSeeds < m.MinSeeders && lastProgress <= graceProgress {
					return nil, fmt.Errorf("swarm too small to stream: %d seeder(s), need %d, and the download is not advancing (%.1f%%)",
						info2.NumSeeds, m.MinSeeders, lastProgress*100)
				}
				// Progress moved, so the swarm is good enough regardless of size.
				if lastProgress > graceProgress {
					graceProgress = lastProgress
				}
			}
		}
		if time.Now().After(deadline) {
			if lastClientErr != nil {
				return nil, fmt.Errorf("torrent client error while preparing playback after %s: %w", timeout, lastClientErr)
			}
			if lastProgress < 0 {
				return nil, fmt.Errorf("torrent metadata not available after %s (no file list from client)", timeout)
			}
			if fragmentedHead {
				// Reporting "still buffering" here would be misleading: the data
				// is arriving fine, it is just not arriving at the front of the
				// file, and the operator needs to know that to act on it.
				return nil, fmt.Errorf("torrent has downloaded %.1f%% of the file but not the first %d bytes continuously after %s (pieces are arriving out of order)",
					lastProgress*100, needHead, timeout)
			}
			return nil, fmt.Errorf("torrent still buffering after %s (file %.1f%% downloaded, need %d bytes of head)",
				timeout, lastProgress*100, needHead)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// categoryHashes returns the set of info hashes currently in this client's
// category, used to tell which torrent an Add actually created.
func (m *Manager) categoryHashes(ctx context.Context, c *qbittorrent.Client) map[string]bool {
	out := map[string]bool{}
	list, err := c.ListCategory(ctx)
	if err != nil {
		return out
	}
	for _, t := range list {
		if h := strings.ToLower(strings.TrimSpace(t.Hash)); h != "" {
			out[h] = true
		}
	}
	return out
}

// resolveAdded identifies the torrent that the preceding Add created.
//
// The known info hash is authoritative when the release carried one. Without it
// the torrent is identified as the one that was not in the category before the
// add; if several appeared at once (concurrent playback requests each adding
// their own), they are disambiguated by matching the torrent name against the
// release title.
//
// It deliberately never falls back to "the newest torrent in the category".
// That guess ignores which torrent was actually requested, so a concurrent add
// by another request would be picked up and served instead — the session, the
// stream list and the release would all be correct while the bytes came from an
// unrelated title.
func (m *Manager) resolveAdded(ctx context.Context, c *qbittorrent.Client, hash string, before map[string]bool, releaseTitle string) (*qbittorrent.TorrentInfo, error) {
	for i := 0; i < 12; i++ {
		if hash != "" {
			if info, err := c.Get(ctx, hash); err == nil && info != nil {
				return info, nil
			}
		} else if list, err := c.ListCategory(ctx); err == nil {
			// An exact name match is the strongest evidence available and is
			// checked first, because it holds whether the add created the
			// torrent or qBittorrent no-opped because it already had it. That
			// second case is a replay of anything previously downloaded, and
			// with no info hash from the indexer there is nothing else to
			// identify it by.
			if existing := exactTitleMatch(list, releaseTitle); existing != nil {
				return existing, nil
			}
			var appeared []qbittorrent.TorrentInfo
			for _, t := range list {
				if !before[strings.ToLower(strings.TrimSpace(t.Hash))] {
					appeared = append(appeared, t)
				}
			}
			// Falling back to what appeared covers a torrent qBittorrent named
			// differently from the indexer's release title. One appearing is
			// only ours if nothing else was added at the same time, so a title
			// check still applies when several did.
			switch {
			case len(appeared) == 1:
				return &appeared[0], nil
			case len(appeared) > 1:
				if best := bestTitleMatch(appeared, releaseTitle); best != nil {
					return best, nil
				}
				logger.Warn("torrent prepare: several torrents appeared at once and none matches the release title",
					"release", releaseTitle, "candidates", len(appeared))
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("could not identify the torrent added for %q", releaseTitle)
}

// stripVideoExt removes a trailing video file extension.
//
// qBittorrent names a single-file torrent after the file itself, extension and
// all, while an indexer's release title has none. Normalising collapses
// separators but keeps the extension as a trailing token, so the two forms
// differ by exactly that and never compare equal. Multi-file torrents are named
// after their folder and carry no extension, which is why only single-file
// releases — most TV episodes — were affected.
func stripVideoExt(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	if _, ok := videoExts[strings.ToLower(ext)]; ok {
		return strings.TrimSuffix(name, ext)
	}
	return name
}

// exactTitleMatch finds a torrent that is the same release as releaseTitle,
// comparing normalised forms so punctuation and separators do not matter.
//
// Deliberately exact rather than the fuzzy overlap used elsewhere. This searches
// the whole category rather than a handful of torrents that just appeared, and
// sibling episodes of a show differ by a single token — "S05E01" against
// "S05E02" shares seven words of eight, which sails past a majority-overlap
// test. Requiring the whole normalised name to agree is what keeps a replay of
// one episode from resolving to another.
//
// Prefers the most complete copy if the same release somehow appears twice.
func exactTitleMatch(list []qbittorrent.TorrentInfo, releaseTitle string) *qbittorrent.TorrentInfo {
	want := release.NormalizeTitleForDedup(stripVideoExt(releaseTitle))
	if want == "" {
		return nil
	}
	var best *qbittorrent.TorrentInfo
	for i := range list {
		if release.NormalizeTitleForDedup(stripVideoExt(list[i].Name)) != want {
			continue
		}
		if best == nil || list[i].Progress > best.Progress {
			best = &list[i]
		}
	}
	return best
}

// bestTitleMatch picks the torrent whose name best matches the release title,
// requiring a majority of the title's words to be present so an unrelated
// torrent is never accepted. Returns nil when nothing matches well enough.
func bestTitleMatch(candidates []qbittorrent.TorrentInfo, releaseTitle string) *qbittorrent.TorrentInfo {
	want := release.NormalizeTitleWordsForMatch(releaseTitle)
	if len(want) == 0 {
		return nil
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	var best *qbittorrent.TorrentInfo
	bestHits := 0
	for i := range candidates {
		hits := 0
		for _, w := range release.NormalizeTitleWordsForMatch(candidates[i].Name) {
			if wantSet[w] {
				hits++
			}
		}
		if hits > bestHits {
			best, bestHits = &candidates[i], hits
		}
	}
	if best == nil || bestHits*2 <= len(wantSet) {
		return nil // too weak a match to trust
	}
	return best
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
