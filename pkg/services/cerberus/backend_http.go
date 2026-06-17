package cerberus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPBackend reports to and queries a central Cerberus server over HTTP.
//
// The wire protocol is intentionally small and stable so a server can be built
// against it later:
//
//	POST {base}/api/v1/report   {"info_hash","reason","status","imdb_id",...}
//	GET  {base}/api/v1/blocked?imdb_id=&tmdb_id=&tvdb_id=&season=&episode=
//	     -> {"hashes": ["...", "..."]}
//
// An optional API key is sent as the Authorization: Bearer header. All calls
// are best-effort from the Client's perspective; this type only implements the
// transport.
type HTTPBackend struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewHTTPBackend builds an HTTPBackend for the given central server base URL
// (e.g. https://cerberus.example.com). Returns nil if baseURL is empty so
// callers can treat "unconfigured" as local-only mode.
func NewHTTPBackend(baseURL, apiKey string) *HTTPBackend {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &HTTPBackend{
		baseURL: baseURL,
		apiKey:  strings.TrimSpace(apiKey),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Name identifies the backend for logging.
func (b *HTTPBackend) Name() string { return "http:" + b.baseURL }

type reportPayload struct {
	InfoHash string `json:"info_hash"`
	Status   string `json:"status"` // "failed" or "healthy"
	Reason   string `json:"reason,omitempty"`
	ImdbID   string `json:"imdb_id,omitempty"`
	TmdbID   string `json:"tmdb_id,omitempty"`
	TvdbID   string `json:"tvdb_id,omitempty"`
	Season   int    `json:"season,omitempty"`
	Episode  int    `json:"episode,omitempty"`
}

// ReportFailure shares a failed info_hash with the central server.
func (b *HTTPBackend) ReportFailure(ctx context.Context, infoHash, reason string, ids ContentIDs) error {
	return b.report(ctx, reportPayload{
		InfoHash: infoHash,
		Status:   "failed",
		Reason:   reason,
		ImdbID:   ids.ImdbID,
		TmdbID:   ids.TmdbID,
		TvdbID:   ids.TvdbID,
		Season:   ids.Season,
		Episode:  ids.Episode,
	})
}

// ReportHealthy shares that an info_hash streamed successfully.
func (b *HTTPBackend) ReportHealthy(ctx context.Context, infoHash string, ids ContentIDs) error {
	return b.report(ctx, reportPayload{
		InfoHash: infoHash,
		Status:   "healthy",
		ImdbID:   ids.ImdbID,
		TmdbID:   ids.TmdbID,
		TvdbID:   ids.TvdbID,
		Season:   ids.Season,
		Episode:  ids.Episode,
	})
}

func (b *HTTPBackend) report(ctx context.Context, payload reportPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/api/v1/report", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	b.auth(req)
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cerberus report: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// BlockedHashes returns community-reported bad hashes for the content.
func (b *HTTPBackend) BlockedHashes(ctx context.Context, ids ContentIDs) ([]string, error) {
	u := fmt.Sprintf("%s/api/v1/blocked?imdb_id=%s&tmdb_id=%s&tvdb_id=%s&season=%d&episode=%d",
		b.baseURL,
		url.QueryEscape(ids.ImdbID), url.QueryEscape(ids.TmdbID), url.QueryEscape(ids.TvdbID),
		ids.Season, ids.Episode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	b.auth(req)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cerberus blocked: unexpected status %d", resp.StatusCode)
	}
	var out struct {
		Hashes []string `json:"hashes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cerberus blocked decode: %w", err)
	}
	return out.Hashes, nil
}

func (b *HTTPBackend) auth(req *http.Request) {
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
}

func drainClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<16))
	_ = rc.Close()
}
