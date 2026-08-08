// Package transmission is a client for the Transmission RPC API.
//
// It exists alongside the qBittorrent client because Transmission 4.1 can do
// one thing qBittorrent cannot: sequential download can be re-anchored to an
// arbitrary piece on a running torrent, which is what steering the download to
// a viewer's seek position requires. qBittorrent's sequential mode is always
// pinned to piece 0 and its WebUI API exposes no way to move it.
//
// # Protocol choice
//
// Transmission 4.1 introduced a JSON-RPC 2.0 protocol with snake_case field
// names and deprecated the older one. This client speaks the OLDER protocol —
// {"method", "arguments", "tag"} with camelCase fields — because 4.1 still
// accepts it and 4.0.x, which is what most seedbox providers still ship, only
// accepts that. Speaking the new protocol would work on exactly one release.
//
// The sequential fields are the exception: they were added after the naming
// changed and exist only as snake_case, in both protocols. Sending
// "sequentialDownload" would be silently ignored — Transmission drops unknown
// arguments without error — so the torrent would download rarest-first with
// nothing to show for it. Verified against libtransmission/quark.cc, which
// carries camelCase and snake_case spellings of every other field and only
// snake_case for these two.
package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"seedstream/pkg/torrent/tclient"
)

// Client talks to one Transmission daemon. Safe for concurrent use.
type Client struct {
	baseURL  string
	username string
	password string
	category string
	savePath string

	http *http.Client

	mu        sync.Mutex
	sessionID string
}

// Options configures a Client.
type Options struct {
	BaseURL  string
	Username string
	Password string
	// Category becomes a Transmission label. Transmission has no categories, but
	// labels serve the same purpose: they mark which torrents are SeedStream's,
	// which is how everything it manages is found again.
	Category string
	SavePath string
	Timeout  time.Duration
}

// New constructs a Transmission client. It performs no network I/O.
func New(opts Options) *Client {
	cat := strings.TrimSpace(opts.Category)
	if cat == "" {
		cat = "seedstream"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL:  strings.TrimRight(opts.BaseURL, "/"),
		username: opts.Username,
		password: opts.Password,
		category: cat,
		savePath: strings.TrimSpace(opts.SavePath),
		http:     &http.Client{Timeout: timeout},
	}
}

func (c *Client) Category() string { return c.category }
func (c *Client) SavePath() string { return c.savePath }

// rpcPath is where the daemon listens. Transmission serves RPC at a fixed path
// under whatever prefix it is mounted on.
const rpcPath = "/transmission/rpc"

type rpcRequest struct {
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
	Tag       int    `json:"tag,omitempty"`
}

type rpcResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
	Tag       int             `json:"tag"`
}

// call performs one RPC round trip.
//
// Transmission guards against cross-site request forgery with a session token:
// the first request of a session is answered 409 with the correct token in a
// header, and is expected to be retried with it. That is not an error
// condition, it is the handshake, so it is retried once transparently.
func (c *Client) call(ctx context.Context, method string, args any, out any) error {
	body, err := json.Marshal(rpcRequest{Method: method, Arguments: args})
	if err != nil {
		return err
	}
	resp, status, err := c.send(ctx, body)
	if status == http.StatusConflict {
		// Handshake: c.send has already stored the new token.
		resp, status, err = c.send(ctx, body)
	}
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("transmission %s: HTTP %d: %s", method, status, strings.TrimSpace(string(resp)))
	}
	var decoded rpcResponse
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return fmt.Errorf("transmission %s decode: %w", method, err)
	}
	if decoded.Result != "success" {
		return fmt.Errorf("transmission %s: %s", method, decoded.Result)
	}
	if out != nil && len(decoded.Arguments) > 0 {
		if err := json.Unmarshal(decoded.Arguments, out); err != nil {
			return fmt.Errorf("transmission %s arguments decode: %w", method, err)
		}
	}
	return nil
}

func (c *Client) send(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+rpcPath, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("X-Transmission-Session-Id", sid)
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("transmission request: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<24))

	if resp.StatusCode == http.StatusConflict {
		if token := resp.Header.Get("X-Transmission-Session-Id"); token != "" {
			c.mu.Lock()
			c.sessionID = token
			c.mu.Unlock()
		}
	}
	return payload, resp.StatusCode, nil
}

// Ping verifies connectivity and credentials.
func (c *Client) Ping(ctx context.Context) error {
	return c.call(ctx, "session-get", map[string]any{"fields": []string{"version"}}, nil)
}

// torrentFields is everything torrent-get is asked for. Requesting a field the
// daemon does not know is an error for the whole call, so this list is limited
// to fields present since Transmission 3.
var torrentFields = []string{
	"id", "hashString", "name", "totalSize", "percentDone", "status",
	"downloadDir", "addedDate", "doneDate", "activityDate", "secondsSeeding",
	"uploadRatio", "uploadedEver", "downloadedEver", "rateDownload", "rateUpload",
	"peersSendingToUs", "pieceCount", "pieceSize", "labels", "trackerStats",
}

type rpcTorrent struct {
	ID               int64    `json:"id"`
	HashString       string   `json:"hashString"`
	Name             string   `json:"name"`
	TotalSize        int64    `json:"totalSize"`
	PercentDone      float64  `json:"percentDone"`
	Status           int      `json:"status"`
	DownloadDir      string   `json:"downloadDir"`
	AddedDate        int64    `json:"addedDate"`
	DoneDate         int64    `json:"doneDate"`
	ActivityDate     int64    `json:"activityDate"`
	SecondsSeeding   int64    `json:"secondsSeeding"`
	UploadRatio      float64  `json:"uploadRatio"`
	UploadedEver     int64    `json:"uploadedEver"`
	DownloadedEver   int64    `json:"downloadedEver"`
	RateDownload     int64    `json:"rateDownload"`
	RateUpload       int64    `json:"rateUpload"`
	PeersSendingToUs int      `json:"peersSendingToUs"`
	PieceCount       int      `json:"pieceCount"`
	PieceSize        int64    `json:"pieceSize"`
	Labels           []string `json:"labels"`
	Pieces           string   `json:"pieces"`
	SequentialDL     bool     `json:"sequential_download"`

	Files []struct {
		Name           string `json:"name"`
		Length         int64  `json:"length"`
		BytesCompleted int64  `json:"bytesCompleted"`
	} `json:"files"`
	FileStats []struct {
		BytesCompleted int64 `json:"bytesCompleted"`
		Wanted         bool  `json:"wanted"`
		Priority       int   `json:"priority"`
	} `json:"fileStats"`
	TrackerStats []struct {
		Announce              string `json:"announce"`
		Host                  string `json:"host"`
		LastAnnounceSucceeded bool   `json:"lastAnnounceSucceeded"`
		LastAnnounceResult    string `json:"lastAnnounceResult"`
		LastAnnounceTime      int64  `json:"lastAnnounceTime"`
		SeederCount           int    `json:"seederCount"`
		LeecherCount          int    `json:"leecherCount"`
	} `json:"trackerStats"`
}

// Transmission torrent status codes.
const (
	statusStopped = 0
	statusSeed    = 6
)

// toTorrentInfo translates Transmission's vocabulary into SeedStream's.
func (c *Client) toTorrentInfo(t rpcTorrent) tclient.TorrentInfo {
	info := tclient.TorrentInfo{
		Hash:         strings.ToLower(t.HashString),
		Name:         t.Name,
		Size:         t.TotalSize,
		Progress:     t.PercentDone,
		State:        stateName(t.Status),
		SavePath:     t.DownloadDir,
		ContentPath:  path.Join(t.DownloadDir, t.Name),
		Category:     firstLabel(t.Labels),
		AddedOn:      t.AddedDate,
		CompletionOn: t.DoneDate,
		SeedingTime:  t.SecondsSeeding,
		LastActivity: t.ActivityDate,
		Ratio:        t.UploadRatio,
		Uploaded:     t.UploadedEver,
		Downloaded:   t.DownloadedEver,
		DlSpeed:      t.RateDownload,
		UpSpeed:      t.RateUpload,
		NumSeeds:     t.PeersSendingToUs,
		PieceSize:    t.PieceSize,
		SequentialDL: t.SequentialDL,
		// Transmission has no equivalent of first/last-piece priority. Reporting
		// it as present would make EnsureStreamingOrder believe there is nothing
		// to do; reporting it absent would make it retry forever. It is set true
		// because there is genuinely nothing to enable — see EnsureStreamingOrder.
		FirstLastPiecePrio: true,
	}
	// Swarm size, from the tracker's own scrape. Transmission reports it per
	// tracker; the largest is the best estimate of the swarm as a whole, since
	// each tracker only knows about the peers it gave out. -1 means not scraped.
	best := -1
	for _, tr := range t.TrackerStats {
		if tr.SeederCount > best {
			best = tr.SeederCount
		}
	}
	if best >= 0 {
		info.NumComplete = &best
	}
	return info
}

func stateName(status int) string {
	switch status {
	case 0:
		return "pausedDL"
	case 1:
		return "checkingUP"
	case 2:
		return "checkingDL"
	case 3:
		return "queuedDL"
	case 4:
		return "downloading"
	case 5:
		return "queuedUP"
	case 6:
		return "uploading"
	default:
		return "unknown"
	}
}

func firstLabel(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return labels[0]
}

// getTorrents fetches torrents, optionally filtered by hash, with extra fields.
func (c *Client) getTorrents(ctx context.Context, hash string, extra ...string) ([]rpcTorrent, error) {
	fields := append(append([]string{}, torrentFields...), extra...)
	args := map[string]any{"fields": fields}
	if h := strings.ToLower(strings.TrimSpace(hash)); h != "" {
		args["ids"] = []string{h}
	}
	var out struct {
		Torrents []rpcTorrent `json:"torrents"`
	}
	if err := c.call(ctx, "torrent-get", args, &out); err != nil {
		return nil, err
	}
	return out.Torrents, nil
}

// Get returns one torrent by info hash, or nil when it is not present.
func (c *Client) Get(ctx context.Context, hash string) (*tclient.TorrentInfo, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, nil
	}
	list, err := c.getTorrents(ctx, hash, "sequential_download")
	if err != nil {
		// A daemon older than 4.1 rejects the whole call for an unknown field,
		// so retry without it rather than losing the torrent entirely. Sequential
		// download then reads as off, which is honest: it is not supported.
		list, err = c.getTorrents(ctx, hash)
		if err != nil {
			return nil, err
		}
	}
	if len(list) == 0 {
		return nil, nil
	}
	info := c.toTorrentInfo(list[0])
	return &info, nil
}

// ListCategory returns every torrent carrying this client's label.
func (c *Client) ListCategory(ctx context.Context) ([]tclient.TorrentInfo, error) {
	list, err := c.getTorrents(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]tclient.TorrentInfo, 0, len(list))
	for _, t := range list {
		if !hasLabel(t.Labels, c.category) {
			continue
		}
		out = append(out, c.toTorrentInfo(t))
	}
	return out, nil
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}

// Add submits a torrent under this client's label, asking for sequential
// download from the start of the file.
func (c *Client) Add(ctx context.Context, opts tclient.AddOptions) error {
	if strings.TrimSpace(opts.URL) == "" {
		return fmt.Errorf("transmission add: empty URL/magnet")
	}
	args := map[string]any{
		"filename": opts.URL,
		"labels":   []string{c.category},
		"paused":   false,
	}
	if c.savePath != "" {
		args["download-dir"] = c.savePath
	}
	if opts.Sequential {
		args["sequential_download"] = true
		args["sequential_download_from_piece"] = 0
	}
	err := c.call(ctx, "torrent-add", args, nil)
	if err != nil && opts.Sequential && isUnknownArgument(err) {
		// Pre-4.1 daemon: no sequential download at all. Adding without it is
		// far better than not adding, and the caller learns the truth from the
		// SequentialDL flag rather than from a failed add.
		delete(args, "sequential_download")
		delete(args, "sequential_download_from_piece")
		return c.call(ctx, "torrent-add", args, nil)
	}
	return err
}

// isUnknownArgument reports whether an RPC error looks like the daemon
// rejecting a field it does not know.
func isUnknownArgument(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown") || strings.Contains(msg, "invalid argument")
}

// Files lists the files within a torrent.
//
// Transmission reports bytes completed per file but nothing about which pieces
// a file spans, so PieceRange is left empty. Availability then falls back to
// refusing an incomplete file, which is the safe answer — see PieceStates for
// the case that matters.
func (c *Client) Files(ctx context.Context, hash string) ([]tclient.FileInfo, error) {
	list, err := c.getTorrents(ctx, hash, "files", "fileStats")
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	t := list[0]
	out := make([]tclient.FileInfo, 0, len(t.Files))
	for i, f := range t.Files {
		done := f.BytesCompleted
		if i < len(t.FileStats) {
			done = t.FileStats[i].BytesCompleted
		}
		progress := 0.0
		if f.Length > 0 {
			progress = float64(done) / float64(f.Length)
		}
		fi := tclient.FileInfo{
			Index:    i,
			Name:     f.Name,
			Size:     f.Length,
			Progress: progress,
		}
		if i < len(t.FileStats) {
			fi.Priority = t.FileStats[i].Priority
		}
		// Piece range, derived rather than reported. Transmission lays files out
		// end to end across the torrent's pieces in listing order, so a running
		// byte offset gives the pieces each file spans exactly as qBittorrent
		// reports them — which is what makes the streaming path work unchanged.
		if t.PieceSize > 0 {
			fi.PieceRange = pieceRangeFor(t, i)
		}
		out = append(out, fi)
	}
	return out, nil
}

// pieceRangeFor computes [firstPiece, lastPiece] for file index idx.
func pieceRangeFor(t rpcTorrent, idx int) []int {
	var offset int64
	for i := 0; i < idx && i < len(t.Files); i++ {
		offset += t.Files[i].Length
	}
	size := t.Files[idx].Length
	if size <= 0 {
		return nil
	}
	first := int(offset / t.PieceSize)
	last := int((offset + size - 1) / t.PieceSize)
	if t.PieceCount > 0 && last >= t.PieceCount {
		last = t.PieceCount - 1
	}
	if last < first {
		return nil
	}
	return []int{first, last}
}

// PieceStates returns the per-piece state for the whole torrent.
//
// Transmission sends a base64 bitfield: one bit per piece, most significant
// bit first within each byte, set when the piece is on disk. It reports only
// "have" or "not have" — there is no in-progress state — so a missing piece
// reads as not downloaded, which is the conservative answer and the only one
// the availability check acts on.
func (c *Client) PieceStates(ctx context.Context, hash string) ([]int, error) {
	list, err := c.getTorrents(ctx, hash, "pieces")
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("transmission pieceStates: torrent %s not found", hash)
	}
	t := list[0]
	raw, err := base64.StdEncoding.DecodeString(t.Pieces)
	if err != nil {
		return nil, fmt.Errorf("transmission pieces decode: %w", err)
	}
	count := t.PieceCount
	if count <= 0 {
		count = len(raw) * 8
	}
	states := make([]int, count)
	for i := 0; i < count; i++ {
		byteIdx := i / 8
		if byteIdx >= len(raw) {
			break
		}
		if raw[byteIdx]&(1<<(7-uint(i%8))) != 0 {
			states[i] = tclient.PieceDownloaded
		}
	}
	return states, nil
}

// Properties returns torrent-level metadata, notably the piece size.
func (c *Client) Properties(ctx context.Context, hash string) (*tclient.TorrentProperties, error) {
	list, err := c.getTorrents(ctx, hash)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	t := list[0]
	return &tclient.TorrentProperties{
		PieceSize:   t.PieceSize,
		PiecesNum:   t.PieceCount,
		TotalSize:   t.TotalSize,
		SeedingTime: t.SecondsSeeding,
	}, nil
}

// TransferInfo returns the daemon's global transfer counters.
func (c *Client) TransferInfo(ctx context.Context) (*tclient.TransferInfo, error) {
	var out struct {
		DownloadSpeed int64 `json:"downloadSpeed"`
		UploadSpeed   int64 `json:"uploadSpeed"`
		CurrentStats  struct {
			UploadedBytes   int64 `json:"uploadedBytes"`
			DownloadedBytes int64 `json:"downloadedBytes"`
		} `json:"current-stats"`
	}
	if err := c.call(ctx, "session-stats", nil, &out); err != nil {
		return nil, err
	}
	return &tclient.TransferInfo{
		UpInfoData:  out.CurrentStats.UploadedBytes,
		DlInfoData:  out.CurrentStats.DownloadedBytes,
		UpInfoSpeed: out.UploadSpeed,
		DlInfoSpeed: out.DownloadSpeed,
	}, nil
}

// Trackers returns the announce state of every tracker on a torrent.
func (c *Client) Trackers(ctx context.Context, hash string) ([]tclient.TrackerInfo, error) {
	list, err := c.getTorrents(ctx, hash)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	out := make([]tclient.TrackerInfo, 0, len(list[0].TrackerStats))
	for _, tr := range list[0].TrackerStats {
		status := tclient.TrackerNotContacted
		switch {
		case tr.LastAnnounceTime <= 0:
			status = tclient.TrackerNotContacted
		case tr.LastAnnounceSucceeded:
			status = tclient.TrackerWorking
		default:
			status = tclient.TrackerNotWorking
		}
		out = append(out, tclient.TrackerInfo{
			URL:    tr.Announce,
			Status: status,
			Msg:    tr.LastAnnounceResult,
		})
	}
	return out, nil
}

// SetFilePriority pulls one file ahead of the rest without deselecting any.
func (c *Client) SetFilePriority(ctx context.Context, hash string, fileIndex, priority int) error {
	key := "priority-normal"
	switch {
	case priority >= 6:
		key = "priority-high"
	case priority <= 1:
		key = "priority-low"
	}
	return c.call(ctx, "torrent-set", map[string]any{
		"ids": []string{strings.ToLower(hash)},
		key:   []int{fileIndex},
	}, nil)
}

// Transmission seeding-limit modes.
const (
	seedModeGlobal    = 0
	seedModeTorrent   = 1
	seedModeUnlimited = 2
)

// SetShareLimits sets when Transmission may stop seeding a torrent by itself.
//
// Transmission expresses this as a mode per criterion rather than a sentinel
// value, so NoShareLimit maps to "unlimited" — the torrent is never stopped for
// that reason, which is what keeps a private tracker's seeding obligation from
// being cut short without SeedStream ever being involved.
//
// It has no total-seed-time limit, only an inactivity one, so seedingTimeMinutes
// has nothing to map to and is ignored. That is safe in the direction that
// matters: it can only mean the torrent seeds for longer, never less.
func (c *Client) SetShareLimits(ctx context.Context, hash string, ratioLimit float64, seedingTimeMinutes, inactiveSeedingTimeMinutes int) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	args := map[string]any{"ids": []string{strings.ToLower(hash)}}

	if ratioLimit == tclient.NoShareLimit {
		args["seedRatioMode"] = seedModeUnlimited
	} else {
		args["seedRatioMode"] = seedModeTorrent
		args["seedRatioLimit"] = ratioLimit
	}
	if inactiveSeedingTimeMinutes == tclient.NoShareLimit {
		args["seedIdleMode"] = seedModeUnlimited
	} else {
		args["seedIdleMode"] = seedModeTorrent
		args["seedIdleLimit"] = inactiveSeedingTimeMinutes
	}
	return c.call(ctx, "torrent-set", args, nil)
}

// EnsureStreamingOrder turns on sequential download for a torrent missing it.
//
// This is where Transmission is better than qBittorrent rather than merely
// equivalent. qBittorrent's endpoints are toggles, so the current state has to
// be read first and a concurrent change can flip the flag the wrong way;
// Transmission takes the value directly, so setting it is idempotent.
//
// There is no first/last-piece priority to enable — Transmission has no such
// concept — so nothing is attempted for it. On a daemon older than 4.1 there is
// no sequential download either, and the unknown-argument error is swallowed:
// it is a property of the deployment, not a fault to report on every poll.
func (c *Client) EnsureStreamingOrder(ctx context.Context, info *tclient.TorrentInfo) error {
	if info == nil || strings.TrimSpace(info.Hash) == "" || info.SequentialDL {
		return nil
	}
	err := c.call(ctx, "torrent-set", map[string]any{
		"ids":                 []string{strings.ToLower(info.Hash)},
		"sequential_download": true,
	}, nil)
	if err != nil {
		if isUnknownArgument(err) {
			return nil // pre-4.1: nothing to enable
		}
		return err
	}
	info.SequentialDL = true
	return nil
}

// SequentialFromPiece re-anchors sequential download at a given piece.
//
// This has no qBittorrent equivalent, and it is the reason this package exists:
// a viewer seeking forward lands in a region the download has not reached, and
// sequential order from piece 0 will walk there in its own time. Moving the
// anchor points the download at where the viewer actually is.
//
// Requires Transmission 4.1; older daemons report an unknown argument, which is
// returned so the caller can tell the difference between "moved" and "cannot".
func (c *Client) SequentialFromPiece(ctx context.Context, hash string, piece int) error {
	if strings.TrimSpace(hash) == "" || piece < 0 {
		return nil
	}
	return c.call(ctx, "torrent-set", map[string]any{
		"ids":                            []string{strings.ToLower(hash)},
		"sequential_download":            true,
		"sequential_download_from_piece": piece,
	}, nil)
}

// Resume starts a paused or stopped torrent.
func (c *Client) Resume(ctx context.Context, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	return c.call(ctx, "torrent-start", map[string]any{
		"ids": []string{strings.ToLower(hash)},
	}, nil)
}

// Pause stops a torrent without removing it or its downloaded data.
func (c *Client) Pause(ctx context.Context, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	return c.call(ctx, "torrent-stop", map[string]any{
		"ids": []string{strings.ToLower(hash)},
	}, nil)
}

// Reannounce asks the daemon to re-contact all trackers for a torrent.
func (c *Client) Reannounce(ctx context.Context, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	return c.call(ctx, "torrent-reannounce", map[string]any{
		"ids": []string{strings.ToLower(hash)},
	}, nil)
}

// Delete removes torrents, optionally with their data.
func (c *Client) Delete(ctx context.Context, hashes []string, deleteFiles bool) error {
	ids := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			ids = append(ids, h)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return c.call(ctx, "torrent-remove", map[string]any{
		"ids":               ids,
		"delete-local-data": deleteFiles,
	}, nil)
}

// Ensure *Client satisfies the interface the manager drives.
var _ tclient.Client = (*Client)(nil)
