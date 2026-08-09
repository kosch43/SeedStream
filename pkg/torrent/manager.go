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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/release"
	"seedstream/pkg/torrent/qbittorrent"
	"seedstream/pkg/torrent/tclient"
	"seedstream/pkg/torrent/transmission"
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
// file before playback starts: the requested buffer, normally capped at a
// tenth of the file, and never more than the file itself. For small files the
// minimum runway wins over the fractional cap.
func requiredHeadBytes(want, fileSize int64) int64 {
	if fileSize <= 0 {
		return want
	}
	if ceiling := int64(float64(fileSize) * StreamableHeadFraction); want > ceiling && ceiling >= MinHeadBytes {
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

// ErrStillBuffering marks a prepare that ran out of time on a torrent that was
// downloading perfectly well — it simply had not finished filling the head yet.
//
// The distinction matters because of what the caller does with a failure. A
// release that cannot work (no video file, a swarm below the floor, the wrong
// title) should be abandoned for the next candidate. A release that merely
// needs longer should not: failing over adds a SECOND torrent for the same
// film, so two copies of a 59 GB remux download side by side, halving the
// bandwidth available to the one the viewer is waiting for. Retrying the same
// slot resumes a download that is already part-finished.
var ErrStillBuffering = errors.New("torrent is still buffering")

// ErrStreamingOrderUnavailable marks a prepare abandoned because sequential
// download could not be confirmed on the torrent.
//
// It shares ErrStillBuffering's meaning for the caller — keep this release,
// retry it — for a different reason. The obstacle is the download client, not
// the release, so the next candidate would be handed to the same client and
// fail the same way. Treating it as a failed candidate walks the whole fallback
// list, starting a download for each, and ends with several copies of one film
// competing for the same bandwidth and none of them ordered any better.
var ErrStreamingOrderUnavailable = errors.New("streaming order could not be enabled")

// KeepReleaseOnRetry reports whether a prepare failure means "try this same
// release again" rather than "try a different one".
//
// The failover loop's real question is not what went wrong but whether another
// candidate could do better. Everything that answers no belongs here, so the
// loop asks once instead of accumulating sentinel checks that drift apart.
func KeepReleaseOnRetry(err error) bool {
	return errors.Is(err, ErrStillBuffering) || errors.Is(err, ErrStreamingOrderUnavailable)
}

// fastCompletionWindow is the point at which waiting for the whole file beats
// waiting for an ordered head.
//
// A contiguous head only appears once the download frontier has passed it AND
// every piece behind it has landed. The gap between those two is the set of
// pieces in flight — libtorrent requests sequentially, but blocks come back from
// dozens of peers at their own pace, so pieces complete out of order within that
// window. Measured on a 72-piece torrent that finished in 24 seconds: the
// contiguous run sat at 7 pieces while overall progress went from 24% to 78%,
// then jumped to 72 at completion.
//
// The size of that window is set by bandwidth and peer count, not by file size.
// On a large torrent it is a small fraction of the file and the ordered head
// arrives early; on one that finishes in seconds it spans nearly the whole file,
// so the ordered head effectively arrives at completion. Waiting for an ordering
// that cannot assert itself before the file is simply finished delays nothing
// and reports a fault that is not there.
const fastCompletionWindow = 30 * time.Second

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
	client tclient.Client
}

// newClient builds the client for one configured entry, or nil when the entry
// names a kind SeedStream cannot drive.
//
// An empty type means qBittorrent. Every configuration written before
// Transmission existed omits the field, and defaulting it keeps those working
// untouched.
func newClient(c config.TorrentClientConfig) tclient.Client {
	if strings.TrimSpace(c.URL) == "" {
		return nil
	}
	clientSavePath := c.SavePath
	if strings.TrimSpace(c.RemotePath) != "" {
		clientSavePath = c.RemotePath
	}
	switch config.NormalizeTorrentClientType(c.Type) {
	case config.TorrentClientQbittorrent:
		return qbittorrent.New(qbittorrent.Options{
			BaseURL:  c.URL,
			Username: c.Username,
			Password: c.Password,
			Category: c.CategoryOrDefault(),
			SavePath: clientSavePath,
		})
	case config.TorrentClientTransmission:
		return transmission.New(transmission.Options{
			BaseURL:  c.URL,
			Username: c.Username,
			Password: c.Password,
			Category: c.CategoryOrDefault(),
			SavePath: clientSavePath,
		})
	default:
		return nil
	}
}

// NewManager builds a Manager from config. Disabled entries, entries with no
// URL, and entries naming an unsupported client are skipped. Returns a Manager
// that reports Enabled()==false when none apply.
func NewManager(clients []config.TorrentClientConfig) *Manager {
	m := &Manager{
		BufferBytes:    DefaultBufferBytes,
		PrepareTimeout: DefaultPrepareTimeout,
	}
	for _, c := range clients {
		if !c.IsEnabled() {
			continue
		}
		client := newClient(c)
		if client == nil {
			continue
		}
		m.clients = append(m.clients, &clientEntry{cfg: c, client: client})
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
	// NumSeeds is how many seeds this client is CONNECTED to. SwarmSeeds is how
	// many the tracker says exist, with SwarmKnown false until it has been
	// scraped. The watchdog judges swarm health on SwarmSeeds where it can:
	// connecting to 4 seeds out of 60 is ordinary BitTorrent behaviour, so
	// treating the connected count as the swarm size condemns healthy torrents
	// and replaces them with worse ones.
	NumSeeds   int
	SwarmSeeds int
	SwarmKnown bool
	// CompletedAt is when the download finished, which is when most trackers
	// start counting a seeding obligation. Zero while still downloading.
	CompletedAt time.Time
	// SeedingHours and Ratio are qBittorrent's own accounting of the obligation
	// so far. They are the client's view, not the tracker's.
	SeedingHours float64
	Ratio        float64
	// SequentialDL and FirstLastPiecePrio are the client's own streaming flags
	// read back from the API. Together they are what makes the head of the file
	// arrive first instead of scattered: sequential download orders the piece
	// requests, and first/last-piece priority raises each wanted file's boundary
	// pieces. Both can be missing on a torrent the add call did not create
	// (re-adds discard the flags), so the watchdog re-reads them every check.
	SequentialDL            bool
	FirstLastPiecePrio      bool
	StreamingOrderSupported bool
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
			swarm, swarmKnown := t.SwarmSeeders()
			out = append(out, TorrentHealthEntry{
				ClientName:              e.cfg.Name,
				Hash:                    t.Hash,
				Name:                    t.Name,
				State:                   t.State,
				Progress:                t.Progress,
				LastActivity:            time.Unix(t.LastActivity, 0),
				AddedAt:                 time.Unix(t.AddedOn, 0),
				NumSeeds:                t.NumSeeds,
				SwarmSeeds:              swarm,
				SwarmKnown:              swarmKnown,
				CompletedAt:             time.Unix(t.CompletionOn, 0),
				SeedingHours:            float64(t.SeedingTime) / 3600,
				Ratio:                   t.Ratio,
				SequentialDL:            t.SequentialDL,
				FirstLastPiecePrio:      t.FirstLastPiecePrio,
				StreamingOrderSupported: t.StreamingOrderSupported,
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

// EnsureStreamingOrder makes the named client's torrent download from the
// front: sequential download on, first/last-piece priority on. The current
// state is read back from the API first and only what is missing is enabled,
// so calling it on a torrent that is already ordered correctly is a no-op.
// Used by the watchdog to repair torrents whose add-time flags were discarded
// (a re-add of a magnet the client already holds) or that were flipped later.
func (m *Manager) EnsureStreamingOrder(ctx context.Context, clientName, hash string) error {
	c := m.clientByName(clientName)
	if c == nil {
		return fmt.Errorf("torrent client %q not found", clientName)
	}
	info, err := c.Get(ctx, hash)
	if err != nil {
		return err
	}
	if info == nil {
		return nil // not present on this client; nothing to order
	}
	return c.EnsureStreamingOrder(ctx, info)
}

// PieceStates returns the per-piece download state of a torrent on the named
// client. This is the only API view of WHERE downloaded bytes are, so it is
// how the watchdog verifies that streaming order is not just enabled but
// actually working for the selected file's piece range.
func (m *Manager) PieceStates(ctx context.Context, clientName, hash string) ([]int, error) {
	c := m.clientByName(clientName)
	if c == nil {
		return nil, fmt.Errorf("torrent client %q not found", clientName)
	}
	return c.PieceStates(ctx, hash)
}

// StreamingHeadStatus identifies the selected video's first torrent piece and
// how much of that file has already landed after it. Piece indexes are global
// to the torrent, so the first piece is not necessarily piece 0 in a season
// pack.
type StreamingHeadStatus struct {
	FileIndex             int
	FileName              string
	FirstPiece            int
	FirstPieceState       int
	DownloadedPiecesAfter int
}

type filePriorityLockEntry struct {
	gate chan struct{}
	refs int
}

var filePriorityLocks = struct {
	sync.Mutex
	entries map[string]*filePriorityLockEntry
}{entries: make(map[string]*filePriorityLockEntry)} // client name + hash

var playbackSelections = struct {
	sync.Mutex
	active map[string]map[int]int // client/hash -> file index -> active stream count
}{active: make(map[string]map[int]int)}

var managedFilePriorities = struct {
	sync.Mutex
	original map[string]map[int]int // client/hash -> file index -> prior priority
}{original: make(map[string]map[int]int)}

const playbackPriorityGrace = 30 * time.Second

type forceStartLease struct {
	active  int
	restore bool
	setter  forceSetter
	hash    string
}

var forceStartLeases = struct {
	sync.Mutex
	leases map[string]*forceStartLease
}{leases: make(map[string]*forceStartLease)}

func acquireFilePriorityLock(ctx context.Context, key string) (func(), error) {
	filePriorityLocks.Lock()
	entry := filePriorityLocks.entries[key]
	if entry == nil {
		entry = &filePriorityLockEntry{gate: make(chan struct{}, 1)}
		filePriorityLocks.entries[key] = entry
	}
	entry.refs++
	filePriorityLocks.Unlock()
	select {
	case entry.gate <- struct{}{}:
		return func() {
			<-entry.gate
			filePriorityLocks.Lock()
			entry.refs--
			if entry.refs == 0 {
				delete(filePriorityLocks.entries, key)
			}
			filePriorityLocks.Unlock()
		}, nil
	case <-ctx.Done():
		filePriorityLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(filePriorityLocks.entries, key)
		}
		filePriorityLocks.Unlock()
		return nil, ctx.Err()
	}
}

func playbackSelectionKey(clientIdentity, hash string) string {
	return strings.TrimSpace(clientIdentity) + "\x00" + strings.ToLower(strings.TrimSpace(hash))
}

func (m *Manager) beginPlaybackSelection(key string, fileIndex int) func() {
	playbackSelections.Lock()
	files := playbackSelections.active[key]
	if files == nil {
		files = make(map[int]int)
		playbackSelections.active[key] = files
	}
	files[fileIndex]++
	playbackSelections.Unlock()

	return func() {
		playbackSelections.Lock()
		defer playbackSelections.Unlock()
		files := playbackSelections.active[key]
		if files == nil {
			return
		}
		if files[fileIndex] <= 1 {
			delete(files, fileIndex)
		} else {
			files[fileIndex]--
		}
		if len(files) == 0 {
			delete(playbackSelections.active, key)
		}
	}
}

func (m *Manager) activePlaybackSelections(key string) map[int]bool {
	playbackSelections.Lock()
	defer playbackSelections.Unlock()
	active := make(map[int]bool, len(playbackSelections.active[key]))
	for fileIndex := range playbackSelections.active[key] {
		active[fileIndex] = true
	}
	return active
}

func rememberManagedPriority(key string, fileIndex, priority int) {
	managedFilePriorities.Lock()
	defer managedFilePriorities.Unlock()
	files := managedFilePriorities.original[key]
	if files == nil {
		files = make(map[int]int)
		managedFilePriorities.original[key] = files
	}
	if _, exists := files[fileIndex]; !exists {
		files[fileIndex] = priority
	}
}

func managedPriority(key string, fileIndex int) (int, bool) {
	managedFilePriorities.Lock()
	defer managedFilePriorities.Unlock()
	priority, ok := managedFilePriorities.original[key][fileIndex]
	return priority, ok
}

func forgetManagedPriority(key string, fileIndex int) {
	managedFilePriorities.Lock()
	defer managedFilePriorities.Unlock()
	files := managedFilePriorities.original[key]
	if files == nil {
		return
	}
	delete(files, fileIndex)
	if len(files) == 0 {
		delete(managedFilePriorities.original, key)
	}
}

// prioritizeActiveVideoFiles gives every actively streamed file maximum
// priority and returns previously selected, now inactive videos to normal.
// Nothing is disabled, so the torrent still completes for seeding. The current
// file list and active set are read while holding a process-wide client/hash
// lock, keeping concurrent playback requests from demoting each other.
func (m *Manager) prioritizeActiveVideoFiles(ctx context.Context, c tclient.Client, key, hash string) error {
	release, err := acquireFilePriorityLock(ctx, key)
	if err != nil {
		return err
	}
	defer release()

	files, err := c.Files(ctx, hash)
	if err != nil {
		return err
	}
	active := m.activePlaybackSelections(key)
	found := make(map[int]bool, len(active))
	for _, f := range files {
		if !active[f.Index] {
			continue
		}
		found[f.Index] = true
		if f.Priority != 7 {
			rememberManagedPriority(key, f.Index, f.Priority)
			if err := c.SetFilePriority(ctx, hash, f.Index, 7); err != nil {
				return fmt.Errorf("raise active file %d priority: %w", f.Index, err)
			}
		}
	}
	for fileIndex := range active {
		if !found[fileIndex] {
			return fmt.Errorf("active file %d no longer exists", fileIndex)
		}
	}
	for _, f := range files {
		original, managed := managedPriority(key, f.Index)
		if active[f.Index] || !managed || !isVideo(f.Name) {
			continue
		}
		if f.Priority != 7 {
			forgetManagedPriority(key, f.Index)
			continue
		}
		if err := c.SetFilePriority(ctx, hash, f.Index, original); err != nil {
			return fmt.Errorf("normalize previous video file %d priority: %w", f.Index, err)
		}
		forgetManagedPriority(key, f.Index)
	}
	return nil
}

func (m *Manager) finishPlaybackSelection(c tclient.Client, key, hash string, release func()) {
	if release == nil {
		return
	}
	time.AfterFunc(playbackPriorityGrace, func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.prioritizeActiveVideoFiles(ctx, c, key, hash); err != nil {
			logger.Debug("torrent playback: could not normalize file priorities after stream ended",
				"hash", shortHash(hash), "err", err)
		}
	})
}

func (m *Manager) releasePlaybackSelectionNow(c tclient.Client, key, hash string, release func()) {
	if release == nil {
		return
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.prioritizeActiveVideoFiles(ctx, c, key, hash); err != nil {
		logger.Debug("torrent playback: could not normalize file priorities after failed prepare",
			"hash", shortHash(hash), "err", err)
	}
}

// StreamingHeads returns the real first-piece state for whichever video files
// the download client currently identifies as playback priorities. A single
// video is unambiguous; in a multi-file torrent, priority 7 is the selection
// made by PrepareForPlayback. Cerberus deliberately observes this live state
// instead of choosing from its persisted content record, which may describe an
// earlier episode from the same season pack while a new one is still buffering.
func (m *Manager) StreamingHeads(ctx context.Context, clientName, hash string) ([]StreamingHeadStatus, error) {
	c := m.clientByName(clientName)
	if c == nil {
		return nil, fmt.Errorf("torrent client %q not found", clientName)
	}
	files, err := c.Files(ctx, hash)
	if err != nil {
		return nil, err
	}
	videos := make([]tclient.FileInfo, 0, len(files))
	for _, f := range files {
		if isVideo(f.Name) {
			videos = append(videos, f)
		}
	}
	if len(videos) == 0 {
		return nil, nil
	}
	selected := make([]tclient.FileInfo, 0, len(videos))
	if len(videos) == 1 {
		selected = append(selected, videos[0])
	} else {
		for _, f := range videos {
			if f.Priority == 7 {
				selected = append(selected, f)
			}
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	states, err := c.PieceStates(ctx, hash)
	if err != nil {
		return nil, err
	}
	out := make([]StreamingHeadStatus, 0, len(selected))
	for _, f := range selected {
		if len(f.PieceRange) < 2 || f.PieceRange[0] < 0 || f.PieceRange[1] < f.PieceRange[0] {
			return nil, fmt.Errorf("torrent client did not report a valid piece range for file %d", f.Index)
		}
		first, last := f.PieceRange[0], f.PieceRange[1]
		if len(states) <= last {
			return nil, fmt.Errorf("piece-state bitmap has %d entries but selected file ends at piece %d", len(states), last)
		}
		downloadedAfter := 0
		for i := first + 1; i <= last; i++ {
			if states[i] == tclient.PieceDownloaded {
				downloadedAfter++
			}
		}
		out = append(out, StreamingHeadStatus{
			FileIndex:             f.Index,
			FileName:              f.Name,
			FirstPiece:            first,
			FirstPieceState:       states[first],
			DownloadedPiecesAfter: downloadedAfter,
		})
	}
	return out, nil
}

type forceSetter interface {
	SetForceStart(context.Context, string, bool) error
}

func acquireForceStartLease(ctx context.Context, key, hash string, setter forceSetter, restore bool) (func(), error) {
	forceStartLeases.Lock()
	lease := forceStartLeases.leases[key]
	if lease == nil {
		lease = &forceStartLease{setter: setter, hash: hash, restore: restore}
		forceStartLeases.leases[key] = lease
	}
	lease.active++
	forceStartLeases.Unlock()
	return func() {
		// beginForceStartLease and this release share the same lifecycle rules.
		forceStartLeases.Lock()
		current := forceStartLeases.leases[key]
		if current == nil {
			forceStartLeases.Unlock()
			return
		}
		current.active--
		if current.active > 0 {
			forceStartLeases.Unlock()
			return
		}
		setter, hash, restore := current.setter, current.hash, current.restore
		if !restore {
			delete(forceStartLeases.leases, key)
			forceStartLeases.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := restoreForceStart(ctx, setter, hash)
		cancel()
		delete(forceStartLeases.leases, key)
		forceStartLeases.Unlock()
		if err != nil {
			logger.Debug("torrent playback: could not restore qBittorrent force-start",
				"hash", shortHash(hash), "err", err)
		}
	}, nil
}

func restoreForceStart(ctx context.Context, setter forceSetter, hash string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := setter.SetForceStart(ctx, hash, false); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func forceStartLeaseActive(key string) bool {
	forceStartLeases.Lock()
	defer forceStartLeases.Unlock()
	lease := forceStartLeases.leases[key]
	return lease != nil && lease.active > 0
}

func isQueuedDownloadState(state string) bool {
	return strings.EqualFold(strings.TrimSpace(state), "queuedDL")
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

// Pause stops a torrent on the named client without deleting its data. The
// Cerberus disk guard uses this when the download filesystem is full.
func (m *Manager) Pause(ctx context.Context, clientName, hash string) error {
	c := m.clientByName(clientName)
	if c == nil {
		return fmt.Errorf("torrent client %q not found", clientName)
	}
	return c.Pause(ctx, hash)
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
func (m *Manager) clientByName(name string) tclient.Client {
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
func (m *Manager) OpenForPlayback(ctx context.Context, res *PrepareResult, ph *Playhead) (io.ReadSeekCloser, error) {
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
	return newSeekableFileReaderWithContext(ctx, f, avail, res.Size, ph), nil
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

	releaseSelection func()
}

// ReleasePlayback releases the selected-file priority ownership acquired by
// PrepareForPlayback after a short grace period. Stremio issues several range
// requests per playback, so releasing on one HTTP response would demote the
// file between requests and make qBittorrent churn priorities.
func (m *Manager) ReleasePlayback(res *PrepareResult) {
	if res != nil && res.releaseSelection != nil {
		res.releaseSelection()
	}
}

// PrepareForPlayback ensures the release's torrent is present in a seedbox
// qBittorrent (adding it if needed), waits for the chosen file's head to buffer,
// and returns the file's absolute path for range serving. The torrent is left
// running so it keeps seeding.
//
// clientOverride, if non-nil, is used instead of the global client list. This
// lets each stream route to its own seedbox qBittorrent.
// profile, when valid, lets the head requirement be revised while waiting. It
// is computed before the torrent exists, so the opening figure rests on a
// bitrate prior; the real download rate only becomes knowable once peers are
// connected, and it ramps — 4 MB/s at t+4s, 112 MB/s at t+20s on a measured
// run. Deciding once at t=0 locks in the answer from the slowest moment.
func (m *Manager) PrepareForPlayback(ctx context.Context, rel *release.Release, season, episode int, bufferBytes int64, profile PlaybackProfile, timeout time.Duration, clientOverride *config.TorrentClientConfig) (*PrepareResult, error) {
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
	var releaseSelection func()
	var successReleases []func()
	var abortReleases []func()
	var releaseOnce sync.Once
	addPlaybackLease := func(onSuccess, onAbort func()) {
		successReleases = append(successReleases, onSuccess)
		abortReleases = append(abortReleases, onAbort)
		if releaseSelection == nil {
			releaseSelection = func() {
				releaseOnce.Do(func() {
					for _, release := range successReleases {
						release()
					}
				})
			}
		}
	}
	selectionTransferred := false
	defer func() {
		if !selectionTransferred && releaseSelection != nil {
			for _, release := range abortReleases {
				release()
			}
		}
	}()

	// Resolve which qBittorrent client to use: prefer the stream-level override
	// so each member can point to their own seedbox, fall back to the first
	// globally configured client.
	var c tclient.Client
	var clientName, clientIdentity, remotePath string
	if clientOverride != nil && strings.TrimSpace(clientOverride.URL) != "" {
		c = qbittorrent.New(qbittorrent.Options{
			BaseURL:  clientOverride.URL,
			Username: clientOverride.Username,
			Password: clientOverride.Password,
			Category: clientOverride.CategoryOrDefault(),
			SavePath: clientOverride.SavePath,
		})
		clientName = strings.TrimSpace(clientOverride.Name)
		clientIdentity = config.NormalizeTorrentClientType(clientOverride.Type) + ":" + strings.TrimRight(strings.TrimSpace(clientOverride.URL), "/")
		remotePath = strings.TrimSpace(clientOverride.RemotePath)
	} else {
		if !m.Enabled() {
			return nil, fmt.Errorf("no torrent client configured")
		}
		c = m.clients[0].client
		clientName = m.clients[0].cfg.Name
		clientIdentity = config.NormalizeTorrentClientType(m.clients[0].cfg.Type) + ":" + strings.TrimRight(strings.TrimSpace(m.clients[0].cfg.URL), "/")
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

	// A complete torrent has no playback ordering work left to do. This also
	// avoids mutating a finished torrent solely to repair flags that no longer
	// affect its data layout.
	if info.Progress < 0.999 && info.StreamingOrderSupported {
		// Whatever route this torrent took into qBittorrent, make it download from
		// the front. A torrent the client already held ignores the streaming flags
		// on the add that "created" it, so without this a re-watch — or anything the
		// user grabbed by hand — fills in rarest-first and never presents a
		// continuous head, no matter how much of it is downloaded. A failure here
		// means the requested streaming invariant cannot be proven, so do not
		// start a player on a potentially sparse file.
		//
		// Marked as a keep-this-release failure: the obstacle is the client, so
		// handing the next candidate to the same client fails identically, and
		// walking the fallback list would start a download for each of them.
		if err := c.EnsureStreamingOrder(ctx, info); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStreamingOrderUnavailable, err)
		}
	}
	forceLeaseAcquired := false
	ensurePlaybackStarted := func(current *qbittorrent.TorrentInfo) error {
		if current == nil || current.Progress >= 0.999 || forceLeaseAcquired {
			return nil
		}
		queued := isQueuedDownloadState(current.State)
		if setter, ok := c.(forceSetter); ok {
			forceKey := playbackSelectionKey(clientIdentity, current.Hash)
			leaseActive := forceStartLeaseActive(forceKey)
			if !queued && !current.ForceStart && !leaseActive {
				return nil
			}
			restoreForceStart := queued && !current.ForceStart && !leaseActive
			leaseRelease, err := acquireForceStartLease(ctx, forceKey, current.Hash, setter, restoreForceStart)
			if err != nil {
				return err
			}
			if err := setter.SetForceStart(ctx, current.Hash, true); err != nil {
				leaseRelease()
				return err
			}
			forceLeaseAcquired = true
			addPlaybackLease(func() {
				time.AfterFunc(playbackPriorityGrace, leaseRelease)
			}, leaseRelease)
			if queued {
				logger.Info("Playback: force-started queued torrent before buffering",
					"hash", shortHash(current.Hash))
			}
			return nil
		}
		if queued {
			if err := c.Resume(ctx, current.Hash); err != nil {
				return err
			}
			forceLeaseAcquired = true
		}
		return nil
	}
	// A client-level "add stopped" preference, or a torrent retained from an
	// earlier session, can leave the selected download motionless even though
	// all of its streaming priorities are correct. Start it before waiting on
	// the piece bitmap; otherwise preparation can only time out on a head that
	// qBittorrent was never asked to fetch.
	if isPausedState(info.State) {
		if err := c.Resume(ctx, info.Hash); err != nil {
			return nil, fmt.Errorf("start torrent for playback: %w", err)
		}
		logger.Info("Playback: resumed stopped torrent before buffering",
			"hash", shortHash(info.Hash), "state", info.State)
	}
	if err := ensurePlaybackStarted(info); err != nil {
		return nil, fmt.Errorf("start torrent for playback: %w", err)
	}

	deadline := time.Now().Add(timeout)
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
	warnedFastFinish := false
	fragmentedHead := false
	fastFinish := false
	// The head actually required, once the file size is known and the ceiling
	// has been applied. Kept out here so it survives across polls — it is
	// revised downward as the real download rate appears — and so a timeout
	// reports the target that was actually in force.
	needHead := bufferBytes
	warnedRevised := false
	// Swarm health: a freshly added torrent needs a moment to find peers, so the
	// live seeder count is only judged after a grace period, and only when the
	// download has also failed to advance — a small but fast swarm is fine.
	started := time.Now()
	graceProgress := -1.0
	selectionKey := ""
	selectionFileIndex := -1
	for {
		if current, getErr := c.Get(ctx, info.Hash); getErr == nil && current != nil {
			*info = *current
			if err := ensurePlaybackStarted(info); err != nil {
				return nil, fmt.Errorf("start torrent for playback: %w", err)
			}
		}
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
				// Priority 7 (maximum) only reorders downloads. Any episode left
				// at 7 by an earlier viewing is returned to normal priority, but no
				// file is disabled, so the torrent still completes and private-
				// tracker seeding obligations stay intact. Best-effort: a failure
				// here just falls back to sequential order.
				if len(files) > 1 {
					if selectionKey == "" {
						selectionKey = playbackSelectionKey(clientIdentity, info.Hash)
						selectionFileIndex = f.Index
						release := m.beginPlaybackSelection(selectionKey, f.Index)
						addPlaybackLease(func() {
							m.finishPlaybackSelection(c, selectionKey, info.Hash, release)
						}, func() {
							m.releasePlaybackSelectionNow(c, selectionKey, info.Hash, release)
						})
					} else if selectionFileIndex != f.Index {
						return nil, fmt.Errorf("selected video file changed from index %d to %d while preparing", selectionFileIndex, f.Index)
					}
					if err := m.prioritizeActiveVideoFiles(ctx, c, selectionKey, info.Hash); err != nil {
						return nil, fmt.Errorf("prioritize selected video file: %w", err)
					}
				}
				// A byte count cannot say where the bytes are. Sequential
				// download orders the REQUESTS, not the arrivals: blocks come
				// back from dozens of peers at their own pace, so pieces within
				// the in-flight window complete out of order. Measured on a real
				// download, the file passed 78% with a continuous run of seven
				// pieces. A head measured as "progress * size >= bufferBytes" is
				// therefore mostly holes, and playback starts, drains the few
				// real pieces, and stalls seconds in.
				//
				// Ask the piece bitmap instead: are the first bufferBytes of the
				// file actually on disk, end to end? That is the same question the
				// reader will ask on its first read, so answering it here means
				// prepare returns only when playback can genuinely begin.
				if avail == nil && f.Size > 0 {
					avail = newFileAvailability(c, info.Hash, f.Index, f.Size)
				}
				needHead = m.reviseHead(ctx, c, info.Hash, needHead,
					requiredHeadBytes(bufferBytes, f.Size), profile, &warnedRevised)

				// A fragmented head is a download whose data is arriving out
				// of order. The download-speed shrink is only safe when data
				// lands sequentially; once the head is known to be fractured,
				// require a deeper runway scaled to the bitrate so playback
				// does not stall seconds in.
				if fragmentedHead {
					floor := MinHeadBytes * 4
					if profile.Valid() {
						if bitrateFloor := int64(profile.BytesPerSecond() * 20); bitrateFloor > floor {
							floor = bitrateFloor
						}
					}
					if needHead < floor {
						needHead = floor
					}
				}

				headReady := false
				if avail != nil {
					headReady = avail.BytesAvailable(ctx, 0, needHead)
				}
				if !headReady && f.Size > 0 {
					// When the whole file is seconds away, a head that is not yet
					// ordered is not worth describing as missing. The in-flight
					// window on a fast download spans most of the file, so the
					// continuous run only completes as the download does: waiting
					// for the file IS waiting for the head, and both arrive at the
					// same moment. Saying otherwise sends an operator hunting for a
					// fault in piece ordering that is not there.
					fast, remain := nearingCompletion(ctx, c, info.Hash, deadline)
					fastFinish = fast
					if fast {
						fragmentedHead = false
						if !warnedFastFinish {
							warnedFastFinish = true
							logger.Info("Playback: this download finishes in moments, so waiting for the whole file rather than for the pieces to arrive in order",
								"hash", shortHash(info.Hash), "progress", f.Progress,
								"seconds_remaining", int(remain.Seconds()))
						}
					} else if done := int64(f.Progress * float64(f.Size)); done >= needHead {
						// Enough bytes, in the wrong places, on a download slow
						// enough that ordering has had time to assert itself.
						// That one is worth reporting.
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
				if fragmentedHead {
					// The file has plenty of bytes but they are scattered —
					// a small head check may pass while the runway beyond
					// it is sparse. Starting on a minimum head that just
					// happens to be contiguous leads to a stall seconds in:
					// the player drains those and hits a hole. Wait for a
					// deeper continuous run before beginning.
					if floor := MinHeadBytes * 4; needHead < floor {
						needHead = floor
						headReady = false
					}
				}
				if headReady {
					abs := absFilePath(remotePath, c.SavePath(), info, f.Name)
					res := &PrepareResult{
						AbsPath:          abs,
						Name:             filepath.Base(f.Name),
						Size:             f.Size,
						Hash:             info.Hash,
						FileIndex:        f.Index,
						ClientName:       clientName,
						Progress:         f.Progress,
						releaseSelection: releaseSelection,
					}
					selectionTransferred = true
					return res, nil
				}
			}
		}
		// Abandon a torrent whose swarm is below the configured minimum, or too
		// thin to ever finish buffering. Waiting out the full timeout on a dead
		// swarm is what makes playback look like it hangs; failing here lets the
		// caller fail over at once.
		//
		// The grace period matters: a freshly added torrent has not announced to
		// its tracker yet, so it reports no swarm at all for the first few
		// seconds. Judging it immediately would reject everything.
		if m.MinSeeders > 0 && lastProgress >= 0 && lastProgress < 0.999 && time.Since(started) > seedCheckGrace {
			if info2, err := c.Get(ctx, info.Hash); err == nil && info2 != nil {
				// The tracker's own live count, when it has been scraped, is the
				// authority — and it is the same quantity the operator's setting
				// is expressed in. It is checked on its own merits: a swarm below
				// the minimum is refused whether or not bytes happen to be
				// arriving, because the setting is a floor on swarm health, not a
				// diagnosis of a stall.
				if swarm, known := info2.SwarmSeeders(); known {
					if swarm < m.MinSeeders {
						return nil, fmt.Errorf("swarm has %d seeder(s), below the %d required to stream",
							swarm, m.MinSeeders)
					}
				} else if graceProgress < 0 {
					graceProgress = lastProgress
				} else {
					// No tracker scrape to go on. Only the connected-seed count is
					// left, and it under-reports by design — BitTorrent connects
					// to a subset of the swarm — so it is trusted only when the
					// download has also failed to advance, which together do mean
					// this is going nowhere.
					if info2.NumSeeds < m.MinSeeders && lastProgress <= graceProgress {
						return nil, fmt.Errorf("swarm too small to stream: %d connected seeder(s), need %d, and the download is not advancing (%.1f%%)",
							info2.NumSeeds, m.MinSeeders, lastProgress*100)
					}
					if lastProgress > graceProgress {
						graceProgress = lastProgress
					}
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
			// Every remaining case is a torrent that is downloading and simply
			// has not got there yet, so all of them wrap ErrStillBuffering: the
			// caller must retry this same torrent rather than start another for
			// the same film.
			if fastFinish {
				// The download was outrunning the clock, not misbehaving. Naming
				// ordering here would point at the wrong thing entirely.
				return nil, fmt.Errorf("%w: still finishing after %s (file %.1f%% downloaded, and downloading fast — the prepare timeout is the limit here, not the swarm)",
					ErrStillBuffering, timeout, lastProgress*100)
			}
			if fragmentedHead {
				// The data is arriving fine, just not at the front of the file,
				// and the operator needs to know that to act on it.
				return nil, fmt.Errorf("%w: downloaded %.1f%% of the file but not the first %d bytes continuously after %s (pieces are arriving out of order)",
					ErrStillBuffering, lastProgress*100, needHead, timeout)
			}
			return nil, fmt.Errorf("%w: after %s the file is %.1f%% downloaded and needs %d bytes of head",
				ErrStillBuffering, timeout, lastProgress*100, needHead)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// reviseHead re-decides how much head to require, now that the torrent's real
// download rate can be observed.
//
// The opening figure was set before the torrent existed, from bitrate alone,
// which cannot tell a seedbox pulling 557 MB/s from a swarm trickling in at 2.
// Both were asked for the same forty-five seconds of video, and on the fast one
// that is several hundred megabytes of waiting for a stream that could have
// started on forty.
//
// Shrinks only, never grows. Growing would move the goalposts mid-wait: a
// momentary dip in the rate would extend a wait already in progress, and the
// caller has a fixed budget to spend. ceiling is the requirement as first
// computed, which nothing may exceed.
func (m *Manager) reviseHead(ctx context.Context, c tclient.Client, hash string, current, ceiling int64, profile PlaybackProfile, warned *bool) int64 {
	if current <= 0 || current > ceiling {
		current = ceiling
	}
	if !profile.Valid() || current <= MinHeadBytes {
		return current
	}
	info, err := c.Get(ctx, hash)
	if err != nil || info == nil || info.DlSpeed <= 0 {
		return current // no rate to go on; the opening figure stands
	}
	want := HeadBytesFor(profile, info.DlSpeed)
	if want >= current {
		return current
	}
	if !*warned {
		*warned = true
		logger.Info("Playback: the download is fast enough to need less of a head than first estimated",
			"hash", shortHash(hash), "dl_bytes_per_sec", info.DlSpeed,
			"playback_bytes_per_sec", int64(profile.BytesPerSecond()),
			"head_was", current, "head_now", want)
	}
	return want
}

// nearingCompletion reports whether the whole torrent will be on disk within
// fastCompletionWindow, and how long that is.
//
// It answers a question the piece bitmap cannot: whether waiting for the head
// to be ordered is a meaningful wait at all. On a download this fast the
// in-flight window covers most of the file, so the continuous run from byte
// zero only completes as the download itself completes — the two conditions
// become the same one, and reporting a fragmented head describes normal swarm
// behaviour as a fault.
//
// Deliberately conservative: an unreadable torrent, an unknown size, or a
// download rate of zero all report false, so the ordinary path is what runs
// whenever this cannot be established.
func nearingCompletion(ctx context.Context, c tclient.Client, hash string, deadline time.Time) (bool, time.Duration) {
	info, err := c.Get(ctx, hash)
	if err != nil || info == nil || info.Size <= 0 || info.DlSpeed <= 0 {
		return false, 0
	}
	remaining := info.Size - int64(info.Progress*float64(info.Size))
	if remaining <= 0 {
		return true, 0
	}
	eta := time.Duration(float64(remaining) / float64(info.DlSpeed) * float64(time.Second))
	if eta <= fastCompletionWindow {
		return true, eta
	}
	if !deadline.IsZero() {
		if budgetLeft := time.Until(deadline); budgetLeft > 0 && eta <= budgetLeft {
			return true, eta
		}
	}
	return false, eta
}

// categoryHashes returns the set of info hashes currently in this client's
// category, used to tell which torrent an Add actually created.
func (m *Manager) categoryHashes(ctx context.Context, c tclient.Client) map[string]bool {
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
func (m *Manager) resolveAdded(ctx context.Context, c tclient.Client, hash string, before map[string]bool, releaseTitle string) (*qbittorrent.TorrentInfo, error) {
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
	// Polling exhausted. A fuzzy match across the whole category would resolve
	// most of these, and must not be used: bestTitleMatch accepts a majority of
	// words, and sibling episodes share every word but one, so it answers a
	// request for S07E01 with S07E02 — the viewer gets the wrong episode and
	// nothing anywhere reports a fault.
	//
	// The case that motivates a fallback here — the client naming a release
	// differently from the indexer — is already handled by exactTitleMatch
	// above, which compares the multiset of words and so survives reordering
	// and alternate spellings without ever accepting a different episode.
	// Failing here is the honest answer for what is left.
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

// titleTokens returns the sorted, normalised words of a release name, which is
// the form two names are compared in.
//
// Sorting is the whole point. The same release is written with its tags in
// different orders by different parties — an indexer publishing
// "Hybrid 2160p UHD BluRay REMUX ... TrueHD 7 1 Atmos" for what the torrent
// itself calls "UHD.BluRay.2160p.TrueHD.Atmos.7.1 ... HYBRID.REMUX" — and
// because normalisation joins words with no delimiter, any reordering yields a
// completely different string. Comparing the sorted words instead tolerates the
// reordering while keeping every word significant.
func titleTokens(name string) []string {
	words := release.NormalizeTitleWordsForMatch(stripVideoExt(name))
	sorted := make([]string, len(words))
	copy(sorted, words)
	sort.Strings(sorted)
	return sorted
}

// sameTokens reports whether two token lists are identical multisets.
func sameTokens(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// exactTitleMatch finds a torrent that is the same release as releaseTitle,
// comparing the multiset of normalised words so punctuation, separators and tag
// ORDER do not matter.
//
// Deliberately exact on the words rather than the fuzzy overlap used elsewhere.
// This searches the whole category rather than a handful of torrents that just
// appeared, and sibling episodes of a show differ by a single token — "S05E01"
// against "S05E02" shares seven words of eight, which sails past a
// majority-overlap test. Requiring every word to be present, with the same
// multiplicity, keeps a replay of one episode from resolving to another while
// surviving a reordering that means nothing.
//
// Failing to match here is expensive: SeedStream adds a torrent, does not
// recognise its own work, reports the candidate as failed and falls back to
// another release — leaving two copies of the same film downloading.
//
// Prefers the most complete copy if the same release somehow appears twice.
func exactTitleMatch(list []qbittorrent.TorrentInfo, releaseTitle string) *qbittorrent.TorrentInfo {
	want := titleTokens(releaseTitle)
	if len(want) == 0 {
		return nil
	}
	var best *qbittorrent.TorrentInfo
	for i := range list {
		if !sameTokens(titleTokens(list[i].Name), want) {
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
	videoCount := 0
	for i := range files {
		if !isVideo(files[i].Name) {
			continue
		}
		videoCount++
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
	if season > 0 && episode > 0 && videoCount > 1 {
		return nil
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
