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

	"seedstream/pkg/torrent/tclient"
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

type streamingOrderLockEntry struct {
	gate chan struct{}
	refs int
}

var streamingOrderLocks = struct {
	sync.Mutex
	entries map[string]*streamingOrderLockEntry
}{entries: make(map[string]*streamingOrderLockEntry)} // WebUI base URL + hash

func acquireStreamingOrderLock(ctx context.Context, baseURL, hash string) (func(), error) {
	key := baseURL + "\x00" + strings.ToLower(strings.TrimSpace(hash))
	streamingOrderLocks.Lock()
	entry := streamingOrderLocks.entries[key]
	if entry == nil {
		entry = &streamingOrderLockEntry{gate: make(chan struct{}, 1)}
		streamingOrderLocks.entries[key] = entry
	}
	entry.refs++
	streamingOrderLocks.Unlock()
	select {
	case entry.gate <- struct{}{}:
		return func() {
			<-entry.gate
			streamingOrderLocks.Lock()
			entry.refs--
			if entry.refs == 0 {
				delete(streamingOrderLocks.entries, key)
			}
			streamingOrderLocks.Unlock()
		}, nil
	case <-ctx.Done():
		streamingOrderLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(streamingOrderLocks.entries, key)
		}
		streamingOrderLocks.Unlock()
		return nil, ctx.Err()
	}
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
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	cat := strings.TrimSpace(opts.Category)
	if cat == "" {
		cat = "seedstream"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL:  baseURL,
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
	// Override the client's "add stopped" preference on both sides of the
	// qBittorrent 5.0 rename. Unknown add fields are ignored, so sending both is
	// compatible with qBittorrent 4.6 (paused) and 5.x (stopped). Queueing is
	// still respected, but putting playback at the top prevents an older queued
	// download from sitting ahead of the stream the viewer just requested.
	form.Set("paused", "false")
	form.Set("stopped", "false")
	form.Set("addToTopOfQueue", "true")
	form.Set("stopCondition", "None")
	if c.savePath != "" {
		form.Set("savepath", c.savePath)
	}
	if opts.Sequential {
		form.Set("sequentialDownload", "true")
		// Sequential mode orders requests, while first/last-piece priority gives
		// each wanted file's boundary region the picker's maximum priority. The
		// manager then raises the selected video above the other files and verifies
		// its actual first piece through pieceStates before playback begins.
		form.Set("firstLastPiecePrio", "true")
	}
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/add", form)
	return err
}

// Types shared with every other download client live in tclient. They are
// aliases, not conversions, so the qBittorrent decoder keeps unmarshalling
// straight into them from its own JSON with no copying at the boundary.
type (
	TorrentInfo       = tclient.TorrentInfo
	FileInfo          = tclient.FileInfo
	TorrentProperties = tclient.TorrentProperties
	TransferInfo      = tclient.TransferInfo
	TrackerInfo       = tclient.TrackerInfo
	AddOptions        = tclient.AddOptions
)

func decodeTorrentInfoList(body []byte) ([]TorrentInfo, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	list := make([]TorrentInfo, len(raw))
	for i, item := range raw {
		if err := json.Unmarshal(item, &list[i]); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return nil, err
		}
		_, hasSequential := fields["seq_dl"]
		_, hasFirstLast := fields["f_l_piece_prio"]
		list[i].StreamingOrderSupported = hasSequential && hasFirstLast
	}
	return list, nil
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
	list, err := decodeTorrentInfoList(body)
	if err != nil {
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
	list, err := decodeTorrentInfoList(body)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent list decode: %w", err)
	}
	return list, nil
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

// Piece states, re-exported from tclient so call sites are unchanged.
const (
	PieceNotDownloaded = tclient.PieceNotDownloaded
	PieceDownloading   = tclient.PieceDownloading
	PieceDownloaded    = tclient.PieceDownloaded
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

// EnsureStreamingOrder turns on sequential download and first/last-piece
// priority for a torrent that is missing either.
//
// Setting them at add time is not enough. Adding a magnet qBittorrent already
// holds succeeds and changes nothing — the flags in that request are thrown
// away — so any torrent that reached the client by another route (an earlier
// build, another tool, the user's own hand) downloads rarest-first. Its bytes
// then land scattered across the file, and a scattered file cannot be streamed
// however much of it is downloaded: at 84% the continuous run from the start
// can still be a few pieces long.
//
// Both endpoints are toggles, so the current state has to be read first;
// calling them unconditionally would switch the flags off on a torrent that
// already had them right.
//
// Race condition: when a torrent is newly added with these flags set, qBittorrent
// may not have processed them yet when Get() is called, returning stale data
// showing the flags as off. The toggle then flips them off after qBittorrent
// processes the add. To handle this, we re-read the state before toggling and
// verify after toggling, retrying if needed.
func (c *Client) EnsureStreamingOrder(ctx context.Context, info *TorrentInfo) error {
	if info == nil || strings.TrimSpace(info.Hash) == "" {
		return nil
	}
	release, err := acquireStreamingOrderLock(ctx, c.baseURL, info.Hash)
	if err != nil {
		return err
	}
	defer release()

	form := url.Values{}
	form.Set("hashes", strings.ToLower(info.Hash))
	readState := func() (bool, error) {
		current, err := c.Get(ctx, info.Hash)
		if err != nil {
			return false, err
		}
		if current == nil {
			return false, fmt.Errorf("qbittorrent streaming order: torrent %s not found", info.Hash)
		}
		info.SequentialDL = current.SequentialDL
		info.FirstLastPiecePrio = current.FirstLastPiecePrio
		return info.SequentialDL && info.FirstLastPiecePrio, nil
	}
	wait := func() error {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}

	for attempt := 0; attempt < 3; attempt++ {
		ready, err := readState()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}

		// A magnet can become visible before its add-time options have settled.
		// Confirm the missing state once before touching toggle endpoints; acting
		// on the first stale read is exactly how an add with both flags enabled can
		// be toggled back off.
		if err := wait(); err != nil {
			return err
		}
		ready, err = readState()
		if err != nil {
			return err
		}
		if ready {
			return nil
		}

		var firstErr error
		if !info.SequentialDL {
			if _, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/toggleSequentialDownload", form); err != nil {
				firstErr = err
			} else {
				info.SequentialDL = true
			}
		}
		if !info.FirstLastPiecePrio {
			if _, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/toggleFirstLastPiecePrio", form); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else {
				info.FirstLastPiecePrio = true
			}
		}
		if firstErr != nil {
			return firstErr
		}

		if err := wait(); err != nil {
			return err
		}
	}

	ready, err := readState()
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("qbittorrent streaming order did not stay enabled for torrent %s (sequential=%t first_last_piece_priority=%t)",
			info.Hash, info.SequentialDL, info.FirstLastPiecePrio)
	}
	return nil
}

// SteerToPiece is a no-op on qBittorrent — its sequential mode is always pinned
// to piece 0 and the WebUI API exposes no method to move it.
func (c *Client) SteerToPiece(_ context.Context, _ string, _ int) error { return nil }

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

// ForceStart starts a torrent without qBittorrent's queue limits. It is used
// only for an active playback download that is otherwise stuck in queuedDL;
// ordinary seeding continues to respect the operator's queue settings.
func (c *Client) ForceStart(ctx context.Context, hash string) error {
	return c.SetForceStart(ctx, hash, true)
}

// SetForceStart explicitly sets qBittorrent's force-start flag. Unlike the
// streaming toggles, this endpoint is idempotent and can restore the prior
// queue behavior when playback no longer needs a queued torrent bypassed.
func (c *Client) SetForceStart(ctx context.Context, hash string, value bool) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.ToLower(hash))
	form.Set("value", strconv.FormatBool(value))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/setForceStart", form)
	return err
}

// Pause stops a torrent. qBittorrent 5.0 renamed the endpoint from
// /torrents/pause to /torrents/stop, so try the new name first and fall back to
// the old one when the server predates the rename.
func (c *Client) Pause(ctx context.Context, hash string) error {
	if strings.TrimSpace(hash) == "" {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.ToLower(hash))
	_, err := c.do(ctx, http.MethodPost, "/api/v2/torrents/stop", form)
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		_, err = c.do(ctx, http.MethodPost, "/api/v2/torrents/pause", form)
	}
	return err
}

// Tracker announce states, re-exported from tclient.
const (
	TrackerDisabled     = tclient.TrackerDisabled
	TrackerNotContacted = tclient.TrackerNotContacted
	TrackerWorking      = tclient.TrackerWorking
	TrackerUpdating     = tclient.TrackerUpdating
	TrackerNotWorking   = tclient.TrackerNotWorking
)

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
const NoShareLimit = tclient.NoShareLimit

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
