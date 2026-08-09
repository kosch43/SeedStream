// Package tclient holds the vocabulary every download client is described in,
// and the interface SeedStream drives them through.
//
// The types were originally qBittorrent's own response shapes, and they still
// read that way: Progress is a fraction, SeedingTime is seconds, piece states
// are the integers qBittorrent reports. A second client implementation
// translates into them rather than the other way round, which keeps the
// translation in one file per client instead of spread through the manager.
package tclient

import "context"

// Piece states, as reported per piece across the whole torrent.
const (
	PieceNotDownloaded = 0
	PieceDownloading   = 1
	PieceDownloaded    = 2
)

// Tracker announce states.
const (
	TrackerDisabled     = 0
	TrackerNotContacted = 1
	TrackerWorking      = 2
	TrackerUpdating     = 3
	TrackerNotWorking   = 4
)

// NoShareLimit disables a share limit, meaning the torrent is never stopped
// automatically on that criterion.
const NoShareLimit = -1

// TorrentInfo describes one torrent.
type TorrentInfo struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Progress     float64 `json:"progress"`
	State        string  `json:"state"`
	SavePath     string  `json:"save_path"`
	ContentPath  string  `json:"content_path"`
	Category     string  `json:"category"`
	AddedOn      int64   `json:"added_on"`
	CompletionOn int64   `json:"completion_on"`
	SeedingTime  int64   `json:"seeding_time"`
	LastActivity int64   `json:"last_activity"`
	Ratio        float64 `json:"ratio"`
	Uploaded     int64   `json:"uploaded"`
	Downloaded   int64   `json:"downloaded"`
	DlSpeed      int64   `json:"dlspeed"`
	UpSpeed      int64   `json:"upspeed"`
	// NumSeeds is how many seeds this client is CONNECTED to; NumComplete is how
	// many exist in the swarm, from the tracker's own scrape. They answer
	// different questions: a healthy torrent routinely shows 4 connected out of
	// 60 in the swarm, because BitTorrent connects to a subset. Judging swarm
	// health on the connected count alone therefore condemns healthy torrents.
	//
	// NumComplete is a pointer so that a server which does not send the field at
	// all is distinguishable from one reporting zero seeders. Decoded into an
	// int it would read as a dead swarm and reject everything.
	NumSeeds    int   `json:"num_seeds"`
	NumComplete *int  `json:"num_complete"`
	NumLeechs   int   `json:"num_leechs"`
	PieceSize   int64 `json:"piece_size"`
	// SequentialDL and FirstLastPiecePrio report the streaming flags. They are
	// only honoured on the torrent that an add actually created: adding a
	// magnet the client already holds is a no-op, so the flags in that request
	// are discarded and the torrent keeps downloading rarest-first. Reading
	// them back is the only way to find out.
	SequentialDL       bool `json:"seq_dl"`
	FirstLastPiecePrio bool `json:"f_l_piece_prio"`
	ForceStart         bool `json:"force_start"`
	// StreamingOrderSupported distinguishes an ordered client reporting "off"
	// from an older client that has no sequential-download capability at all.
	StreamingOrderSupported bool `json:"-"`
}

// SwarmSeeders returns how many seeders the tracker says the swarm holds, and
// whether that is actually known.
//
// This is the number to judge a swarm on. The indexer's count comes from a
// scrape that may be hours old and is whatever the tracker chose to publish;
// this one is the client's own current scrape of the same tracker. Where the
// two disagree, this is the one that reflects the swarm being downloaded from.
//
// Reports false before the first scrape completes, and on a server that omits
// the field entirely, so an unknown swarm is never mistaken for an empty one.
func (t *TorrentInfo) SwarmSeeders() (int, bool) {
	if t == nil || t.NumComplete == nil || *t.NumComplete < 0 {
		return 0, false
	}
	return *t.NumComplete, true
}

// FileInfo describes one file within a torrent.
type FileInfo struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
	// PieceRange is [firstPiece, lastPiece] — the torrent-global indices of the
	// pieces this file spans. Empty when the client cannot report it, in which
	// case callers must fall back to Progress.
	PieceRange []int `json:"piece_range"`
}

// TorrentProperties is torrent-level metadata, notably the piece size needed to
// map byte offsets onto piece indices.
type TorrentProperties struct {
	PieceSize   int64 `json:"piece_size"`
	PiecesNum   int   `json:"pieces_num"`
	TotalSize   int64 `json:"total_size"`
	SeedingTime int64 `json:"seeding_time"`
}

// TransferInfo is the client's global transfer counters.
type TransferInfo struct {
	UpInfoData  int64 `json:"up_info_data"`
	DlInfoData  int64 `json:"dl_info_data"`
	UpInfoSpeed int64 `json:"up_info_speed"`
	DlInfoSpeed int64 `json:"dl_info_speed"`
}

// TrackerInfo is the announce state of one tracker on a torrent.
type TrackerInfo struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

// AddOptions controls how a torrent is added.
type AddOptions struct {
	// URL is a magnet link or an http(s) .torrent URL. Required.
	URL string
	// Sequential asks the client to download pieces in order, so the start of
	// the file is ready first for progressive playback.
	Sequential bool
}

// Client is what SeedStream needs a download client to do.
//
// Everything here is either read-only or additive. Nothing in this interface
// stops a torrent seeding or removes data except Delete, which exists for the
// operator rather than for any automatic path.
type Client interface {
	// Ping checks the client is reachable and the credentials work.
	Ping(ctx context.Context) error

	// Add submits a torrent under this client's configured category.
	Add(ctx context.Context, opts AddOptions) error
	// Get returns one torrent by info hash, or nil when it is not present.
	Get(ctx context.Context, hash string) (*TorrentInfo, error)
	// ListCategory returns every torrent in this client's category.
	ListCategory(ctx context.Context) ([]TorrentInfo, error)

	// Files lists the files within a torrent.
	Files(ctx context.Context, hash string) ([]FileInfo, error)
	// PieceStates returns the per-piece state for the whole torrent. This is
	// the only call that says WHERE downloaded bytes are, as opposed to how
	// many of them there are, so streaming correctness rests on it.
	PieceStates(ctx context.Context, hash string) ([]int, error)
	// Properties returns torrent-level metadata, notably the piece size.
	Properties(ctx context.Context, hash string) (*TorrentProperties, error)
	// TransferInfo returns the client's global transfer counters.
	TransferInfo(ctx context.Context) (*TransferInfo, error)
	// Trackers returns the announce state of every tracker on a torrent.
	Trackers(ctx context.Context, hash string) ([]TrackerInfo, error)

	// SetFilePriority pulls one file of a multi-file torrent ahead of the rest.
	// It never deselects the others, so the torrent still completes and any
	// seeding obligation stays intact.
	SetFilePriority(ctx context.Context, hash string, fileIndex, priority int) error
	// SetShareLimits sets when the client may stop seeding a torrent by itself.
	// Pass NoShareLimit to mean "never stop for this reason".
	SetShareLimits(ctx context.Context, hash string, ratioLimit float64, seedingTimeMinutes, inactiveSeedingTimeMinutes int) error
	// EnsureStreamingOrder turns on whatever ordering the client offers for a
	// torrent that is missing it. Adding a torrent the client already holds
	// discards the flags on that request, so this is the only way a torrent
	// that arrived by another route ever gets them.
	EnsureStreamingOrder(ctx context.Context, info *TorrentInfo) error

	// SteerToPiece re-anchors sequential download at a given torrent piece so
	// the download follows the viewer's position. qBittorrent's sequential mode
	// is always pinned to piece 0, so its implementation is a no-op.
	// Transmission supports re-anchoring to an arbitrary piece (4.1+); older
	// daemons return the unknown-argument error, which the caller treats as
	// best-effort.
	SteerToPiece(ctx context.Context, hash string, piece int) error

	// Resume starts a paused or stopped torrent.
	Resume(ctx context.Context, hash string) error
	// Pause stops a torrent without deleting it or its downloaded data.
	Pause(ctx context.Context, hash string) error
	// Reannounce asks the client to re-contact all trackers for a torrent.
	Reannounce(ctx context.Context, hash string) error
	// Delete removes torrents, optionally with their data.
	Delete(ctx context.Context, hashes []string, deleteFiles bool) error

	// Category is the label SeedStream's own torrents carry.
	Category() string
	// SavePath is where the client writes downloads, as the client sees it.
	SavePath() string
}
