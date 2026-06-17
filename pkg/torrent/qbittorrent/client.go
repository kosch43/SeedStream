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
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qbittorrent login failed: HTTP %d", resp.StatusCode)
	}
	if strings.Contains(strings.ToLower(string(body)), "fails") {
		return "", fmt.Errorf("qbittorrent login failed (check credentials / WebUI host whitelist)")
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "SID" {
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
