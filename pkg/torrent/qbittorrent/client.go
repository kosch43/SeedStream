// Package qbittorrent is a minimal client for the qBittorrent WebUI API (v2).
//
// SeedStream uses it to hand a picked torrent to a qBittorrent instance running
// on a seedbox so the torrent downloads sequentially (for instant playback) and
// then keeps seeding indefinitely. SeedStream never seeds itself; the seedbox's
// qBittorrent owns ratio/H&R compliance for private trackers.
package qbittorrent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to one qBittorrent WebUI. It is safe for concurrent use.
type Client struct {
	baseURL  string
	username string
	password string
	category string
	savePath string

	http *http.Client

	mu        sync.Mutex
	cookie    string
	cookieSet time.Time
}

// Options configures a Client.
type Options struct {
	BaseURL  string
	Username string
	Password string
	Category string // applied to added torrents; defaults to "seedstream"
	SavePath string // absolute download path on the seedbox; "" = client default
	Timeout  time.Duration
}

// New constructs a qBittorrent client. It does not perform any network I/O.
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

// Category returns the category applied to torrents this client adds.
func (c *Client) Category() string { return c.category }

// SavePath returns the configured download path (may be empty).
func (c *Client) SavePath() string { return c.savePath }

const cookieTTL = 25 * time.Minute

func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cookie != "" && time.Since(c.cookieSet) < cookieTTL {
		return c.cookie, nil
	}

	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.baseURL)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("qbittorrent login request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("qbittorrent login failed: HTTP %d", resp.StatusCode)
	}
	if strings.Contains(strings.ToLower(string(body)), "fails") {
		return "", fmt.Errorf("qbittorrent login failed (check credentials / WebUI host whitelist)")
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "SID" || strings.HasPrefix(ck.Name, "QBT_SID_") {
			c.cookie = ck.Name + "=" + ck.Value
			c.cookieSet = time.Now()
			return c.cookie, nil
		}
	}
	return "", fmt.Errorf("qbittorrent login: no SID cookie returned")
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	cookie, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if form != nil {
		bodyReader = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", c.baseURL)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		// Cookie likely expired; drop it so the next call re-authenticates.
		c.mu.Lock()
		c.cookie = ""
		c.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("qbittorrent %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// Ping verifies connectivity and credentials.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/api/v2/app/version", nil)
	return err
}

// AddOptions controls how a torrent is added.
type AddOptions struct {
	// Magnet or URL (magnet link, http(s) .torrent URL). Required.
	URL string
	// Sequential makes qBittorrent download pieces in order, so the start of the
	// file is ready first for progressive playback.
	Sequential bool
}

// Add submits a torrent to qBittorrent under the configured category, with
// sequential download and first/last-piece priority enabled for streaming.
// Auto Torrent Management is left off so SavePath is honoured.
func (c *Client) Add(ctx context.Context, opts AddOptions) error {
	if strings.TrimSpace(opts.URL) == "" {
		return fmt.Errorf("qbittorrent add: empty URL/magnet")
	}
	form := url.Values{}
	form.Set("urls", opts.URL)
	form.Set("category", c.category)
	form.Set("autoTMM", "false")
	if c.savePath != "" {
		form.Set("savepath", c.savePath)
	}
	if opts.Sequential {
		form.Set("sequentialDownload", "true")
		// First/last-piece priority is deliberate, not a leftover. Players read
		// the tail of an MP4/MKV early — for the index and duration — and a seek
		// to the end of the file is a normal thing for a user to do; without the
		// last piece both stall for as long as the swarm takes to reach the end
		// of a sequential download. It costs one piece at each end and does make
		// the file sparse from the outset, which is precisely why availability is
		// decided from the piece bitmap rather than from a byte count.
		form.Set("firstLastPiecePrio", "true")
	}
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/add", form)
	return err
}

// TorrentInfo is a subset of qBittorrent's /torrents/info response.
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
	NumSeeds     int     `json:"num_seeds"`
	NumLeechs    int     `json:"num_leechs"`
	PieceSize    int64   `json:"piece_size"`
}

// Get returns the torrent with the given info hash, or nil if not present.
func (c *Client) Get(ctx context.Context, hash string) (*TorrentInfo, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, nil
	}
	body, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/info?hashes="+url.QueryEscape(strings.ToLower(hash)), nil)
	if err != nil {
		return nil, err
	}
	var list []TorrentInfo
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("qbittorrent info decode: %w", err)
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// ListCategory returns all torrents in this client's category.
func (c *Client) ListCategory(ctx context.Context) ([]TorrentInfo, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/info?category="+url.QueryEscape(c.category), nil)
	if err != nil {
		return nil, err
	}
	var list []TorrentInfo
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("qbittorrent list decode: %w", err)
	}
	return list, nil
}

// FileInfo is a subset of qBittorrent's /torrents/files response.
type FileInfo struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
	// PieceRange is [firstPiece, lastPiece] — the torrent-global indices of the
	// pieces this file spans. Present on qBittorrent >= 4.4; empty on older
	// versions, in which case callers must fall back to Progress.
	PieceRange []int `json:"piece_range"`
}

// Files lists the files within a torrent.
func (c *Client) Files(ctx context.Context, hash string) ([]FileInfo, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/files?hash="+url.QueryEscape(strings.ToLower(hash)), nil)
	if err != nil {
		return nil, err
	}
	var list []FileInfo
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("qbittorrent files decode: %w", err)
	}
	// qBittorrent omits index on older versions; backfill from slice order.
	for i := range list {
		if list[i].Index == 0 && i != 0 {
			list[i].Index = i
		}
	}
	return list, nil
}

// Piece states as reported by /torrents/pieceStates.
const (
	PieceNotDownloaded = 0
	PieceDownloading   = 1
	PieceDownloaded    = 2
)

// PieceStates returns the per-piece download state for the whole torrent:
// one entry per piece, PieceNotDownloaded/PieceDownloading/PieceDownloaded.
// This is the only qBittorrent API that says WHERE downloaded bytes are, as
// opposed to how many of them there are.
func (c *Client) PieceStates(ctx context.Context, hash string) ([]int, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/pieceStates?hash="+url.QueryEscape(strings.ToLower(hash)), nil)
	if err != nil {
		return nil, err
	}
	var states []int
	if err := json.Unmarshal(body, &states); err != nil {
		return nil, fmt.Errorf("qbittorrent pieceStates decode: %w", err)
	}
	return states, nil
}

// TorrentProperties is a subset of /torrents/properties.
type TorrentProperties struct {
	PieceSize   int64 `json:"piece_size"`
	PiecesNum   int   `json:"pieces_num"`
	TotalSize   int64 `json:"total_size"`
	SeedingTime int64 `json:"seeding_time"`
}

// Properties returns torrent-level metadata, notably the piece size needed to
// map byte offsets onto piece indices.
func (c *Client) Properties(ctx context.Context, hash string) (*TorrentProperties, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/properties?hash="+url.QueryEscape(strings.ToLower(hash)), nil)
	if err != nil {
		return nil, err
	}
	var props TorrentProperties
	if err := json.Unmarshal(body, &props); err != nil {
		return nil, fmt.Errorf("qbittorrent properties decode: %w", err)
	}
	return &props, nil
}

// TransferInfo is a subset of /transfer/info — the client's global transfer
// counters. UpInfoData/DlInfoData are the bytes transferred in the current
// qBittorrent session, so they reset to 0 whenever qBittorrent restarts;
// callers that want a running total must accumulate positive deltas.
type TransferInfo struct {
	UpInfoData  int64 `json:"up_info_data"`
	DlInfoData  int64 `json:"dl_info_data"`
	UpInfoSpeed int64 `json:"up_info_speed"`
	DlInfoSpeed int64 `json:"dl_info_speed"`
}

// TransferInfo returns the client's global transfer counters, used by the
// upload guard to track how much has been seeded this billing period.
func (c *Client) TransferInfo(ctx context.Context) (*TransferInfo, error) {
	body, err := c.do(ctx, http.MethodGet, "/api/v2/transfer/info", nil)
	if err != nil {
		return nil, err
	}
	var info TransferInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("qbittorrent transfer info decode: %w", err)
	}
	return &info, nil
}

// SetFilePriority sets the download priority of one file within a torrent.
// Priority 7 (maximum) makes qBittorrent fetch that file's pieces first, which
// is how a specific episode of a season pack gets pulled ahead of the rest.
// It never disables other files — everything still downloads, so the torrent
// completes and private-tracker seeding obligations stay intact.
func (c *Client) SetFilePriority(ctx context.Context, hash string, fileIndex, priority int) error {
	form := url.Values{}
	form.Set("hash", strings.ToLower(hash))
	form.Set("id", strconv.Itoa(fileIndex))
	form.Set("priority", strconv.Itoa(priority))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/filePrio", form)
	return err
}

// Resume starts a paused/stopped torrent. qBittorrent 5.0 renamed the endpoint
// from /torrents/resume to /torrents/start, so try the new name first and fall
// back to the old one when the server predates the rename.
func (c *Client) Resume(ctx context.Context, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.ToLower(hash))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/start", form)
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		_, err = c.do(ctx, http.MethodPost, "/api/v2/torrents/resume", form)
	}
	return err
}

// Tracker announce states reported by /torrents/trackers.
const (
	TrackerDisabled     = 0 // DHT/PeX/LSD pseudo-entries
	TrackerNotContacted = 1
	TrackerWorking      = 2
	TrackerUpdating     = 3
	TrackerNotWorking   = 4
)

// TrackerInfo is a subset of /torrents/trackers.
type TrackerInfo struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

// Trackers returns the announce state of every tracker on a torrent.
//
// This is the only way to tell whether the tracker is actually receiving our
// announces. Seeding time and ratio are counted locally by qBittorrent, so a
// torrent whose announces have been failing will still accumulate both while
// the tracker records nothing — which is exactly the situation in which trusting
// local counters would breach a hit-and-run obligation.
func (c *Client) Trackers(ctx context.Context, hash string) ([]TrackerInfo, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, nil
	}
	body, err := c.do(ctx, http.MethodGet, "/api/v2/torrents/trackers?hash="+url.QueryEscape(strings.ToLower(hash)), nil)
	if err != nil {
		return nil, err
	}
	var list []TrackerInfo
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("qbittorrent trackers decode: %w", err)
	}
	return list, nil
}

// NoShareLimit disables a qBittorrent share limit, meaning the torrent is never
// stopped automatically on that criterion.
const NoShareLimit = -1

// SetShareLimits sets when qBittorrent may stop seeding a torrent by itself.
//
// qBittorrent applies global ratio and seeding-time limits that pause or even
// delete a torrent once reached. On a private tracker that can cut seeding short
// of a hit-and-run obligation without SeedStream ever being involved, so torrents
// SeedStream adds have their limits pinned rather than inheriting the global
// defaults. Pass NoShareLimit to mean "never stop for this reason".
//
// ratioLimit is an upload/download ratio; seedingTimeMinutes and
// inactiveSeedingTimeMinutes are in minutes.
func (c *Client) SetShareLimits(ctx context.Context, hash string, ratioLimit float64, seedingTimeMinutes, inactiveSeedingTimeMinutes int) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.ToLower(hash))
	form.Set("ratioLimit", strconv.FormatFloat(ratioLimit, 'f', -1, 64))
	form.Set("seedingTimeLimit", strconv.Itoa(seedingTimeMinutes))
	form.Set("inactiveSeedingTimeLimit", strconv.Itoa(inactiveSeedingTimeMinutes))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/setShareLimits", form)
	return err
}

// Delete removes torrents, optionally deleting their downloaded files. Used by
// ratio-safe cache eviction; callers must respect seed-time before deleting.
func (c *Client) Delete(ctx context.Context, hashes []string, deleteFiles bool) error {
	if len(hashes) == 0 {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.ToLower(strings.Join(hashes, "|")))
	if deleteFiles {
		form.Set("deleteFiles", "true")
	} else {
		form.Set("deleteFiles", "false")
	}
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/delete", form)
	return err
}

// Reannounce asks qBittorrent to re-contact all trackers for the given
// torrent, which can revive a stalled download when new seeds join the swarm.
func (c *Client) Reannounce(ctx context.Context, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.ToLower(hash))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/reannounce", form)
	return err
}
