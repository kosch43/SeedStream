package cardigann

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"seedstream/pkg/core/config"
	"seedstream/pkg/core/logger"
	"seedstream/pkg/indexer/httpproxy"
)

// Engine drives one tracker: it holds the definition, the operator's
// credentials, and the logged-in session.
// defaultUserAgent is deliberately NOT a browser string.
//
// Claiming to be Chrome while presenting Go's TLS and HTTP/2 fingerprints is a
// mismatch Cloudflare detects and challenges: measured against TorrentLeech,
// the Chrome UA returns 403 with `cf-mitigated: challenge` on every request —
// GET and POST, HTTP/1.1 and HTTP/2, and with the full Sec-Fetch/sec-ch-ua set
// attached. An honest non-browser agent from the same IP returns 200. Passing
// as a browser needs a matching TLS fingerprint, which this client cannot
// provide, so the reachable option is not to pretend.
//
// Trackers that genuinely gate on a browser UA can set query_header per
// indexer.
const defaultUserAgent = "SeedStream/1.0 (+https://github.com/kosch43/SeedStream)"

type Engine struct {
	def     *Definition
	baseURL string
	config  map[string]string

	// userAgent is sent on every request. Per-indexer query_header overrides
	// the default when a tracker needs something specific.
	userAgent string

	http *http.Client

	mu          sync.Mutex
	loggedIn    bool
	lastLoginAt time.Time

	// throttle serialises requests to this tracker and holds the time the last
	// one went out, so the tracker's own declared minimum interval is honoured.
	// Separate from mu, which guards login state and is held across a login.
	throttle    sync.Mutex
	lastRequest time.Time
}

// waitForTurn blocks until this tracker's declared request delay has elapsed
// since the previous request, and reserves the slot for this one.
//
// 65 of the bundled definitions declare requestDelay — TorrentLeech asks for
// 4.1 seconds — and it was parsed nowhere and honoured nowhere. Jackett and
// Prowlarr both obey it. Ignoring it on a private tracker is how an account
// gets rate-limited or banned, and searches now run concurrently, so without
// this several requests leave for the same tracker at once.
//
// The lock is held across the wait deliberately: that is what turns a burst of
// concurrent searches into a queue rather than letting them all observe the
// same stale timestamp and fire together.
func (e *Engine) waitForTurn(ctx context.Context) error {
	delay := time.Duration(e.def.RequestDelay * float64(time.Second))
	if delay <= 0 {
		return nil
	}
	e.throttle.Lock()
	defer e.throttle.Unlock()

	if wait := delay - time.Since(e.lastRequest); wait > 0 && !e.lastRequest.IsZero() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	e.lastRequest = time.Now()
	return nil
}

// loginTTL bounds how long a session is trusted before it is re-tested, so an
// expired cookie is noticed without re-logging in on every search.
const loginTTL = 30 * time.Minute

// NewEngine builds an engine for a definition. baseURLOverride lets the operator
// point at a different domain when a tracker moves, without editing the file.
// The optional indexer config preserves the legacy constructor while allowing
// callers to apply the same proxy and verified TLS policy as Torznab clients.
func NewEngine(def *Definition, baseURLOverride string, settings map[string]string, timeout time.Duration, indexerConfigs ...config.IndexerConfig) (*Engine, error) {
	if def == nil {
		return nil, fmt.Errorf("nil definition")
	}
	base := def.BaseURL(baseURLOverride)
	if base == "" {
		return nil, fmt.Errorf("definition %q has no usable base URL", def.ID)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var indexerCfg config.IndexerConfig
	if len(indexerConfigs) > 0 {
		indexerCfg = indexerConfigs[0]
	}
	tlsConfig, err := indexerCfg.TLSClientConfig()
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	for k, v := range settings {
		cfg[k] = v
	}
	// Defaults from the definition fill in anything the operator left blank.
	for _, s := range def.Settings {
		if _, ok := cfg[s.Name]; !ok && s.Default != "" {
			cfg[s.Name] = s.Default
		}
	}
	transport := &http.Transport{
		Proxy:               httpproxy.IndexerProxy(indexerCfg.ProxyURL),
		TLSClientConfig:     tlsConfig,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	}
	ua := strings.TrimSpace(indexerCfg.QueryHeader)
	if ua == "" {
		ua = defaultUserAgent
	}
	return &Engine{
		def:       def,
		baseURL:   base,
		config:    cfg,
		userAgent: ua,
		http:      &http.Client{Jar: jar, Timeout: timeout, Transport: transport},
	}, nil
}

// Definition exposes the definition this engine drives.
func (e *Engine) Definition() *Definition { return e.def }

// BaseURL is the site address in use, after any override.
func (e *Engine) BaseURL() string { return e.baseURL }

func (e *Engine) newContext() *Context {
	return &Context{
		Config:  e.config,
		Query:   map[string]string{},
		Result:  map[string]string{},
		BaseURL: strings.TrimRight(e.baseURL, "/"),
		Today:   time.Now(),
	}
}

// do issues a request with the browser-like headers trackers expect.
func (e *Engine) do(ctx context.Context, method, rawURL string, form url.Values, headers map[string]string) (*goquery.Document, string, error) {
	// Respect the tracker's own request-rate limit before anything leaves.
	if err := e.waitForTurn(ctx); err != nil {
		return nil, "", err
	}
	var body io.Reader
	if form != nil && method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", e.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "max-age=0")
	if form != nil && method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", strings.TrimRight(e.baseURL, "/"))
		req.Header.Set("Referer", rawURL)
	} else {
		req.Header.Set("Referer", strings.TrimRight(e.baseURL, "/")+"/")
	}
	// A pasted session cookie is how trackers behind a captcha are supported.
	if c := strings.TrimSpace(e.config["cookie"]); c != "" {
		req.Header.Set("Cookie", c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 400 {
		return nil, string(raw), fmt.Errorf("%s %s: HTTP %d", method, rawURL, resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(raw)))
	if err != nil {
		return nil, string(raw), err
	}
	return doc, string(raw), nil
}

// Login authenticates if the definition requires it, reusing a live session.
func (e *Engine) Login(ctx context.Context) error {
	if !e.def.RequiresLogin() {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loggedIn && time.Since(e.lastLoginAt) < loginTTL {
		return nil
	}
	// A pasted cookie replaces the login flow entirely.
	if strings.TrimSpace(e.config["cookie"]) != "" {
		e.loggedIn = true
		e.lastLoginAt = time.Now()
		return nil
	}
	if e.def.NeedsCaptcha() {
		return fmt.Errorf("%s requires solving a captcha to log in — paste a session cookie instead", e.def.Name)
	}

	l := e.def.Login
	ctxv := e.newContext()
	loginURL := ResolveURL(e.baseURL, ctxv.Expand(l.Path))

	form := url.Values{}
	for k, v := range l.Inputs {
		form.Set(k, ctxv.Expand(v))
	}

	method := strings.ToLower(strings.TrimSpace(l.Method))
	switch method {
	case "post", "form":
		// A form login must carry the page's hidden fields (CSRF tokens and the
		// like), so the page is fetched first and its inputs merged in.
		if method == "form" {
			if doc, _, err := e.do(ctx, http.MethodGet, loginURL, nil, nil); err == nil {
				sel := l.Form
				if strings.TrimSpace(sel) == "" {
					sel = "form"
				}
				doc.Find(sel).First().Find("input").Each(func(_ int, s *goquery.Selection) {
					name, ok := s.Attr("name")
					if !ok || name == "" {
						return
					}
					if _, given := l.Inputs[name]; given {
						return
					}
					val, _ := s.Attr("value")
					form.Set(name, val)
				})
			}
			if p := strings.TrimSpace(l.SubmitPath); p != "" {
				loginURL = ResolveURL(e.baseURL, ctxv.Expand(p))
			}
		}
		doc, raw, err := e.do(ctx, http.MethodPost, loginURL, form, nil)
		if err != nil {
			return fmt.Errorf("%s login: %w", e.def.Name, err)
		}
		if msg := e.loginError(doc, raw); msg != "" {
			return fmt.Errorf("%s login rejected: %s", e.def.Name, msg)
		}
	case "get":
		u := loginURL
		if len(form) > 0 {
			sep := "?"
			if strings.Contains(u, "?") {
				sep = "&"
			}
			u += sep + form.Encode()
		}
		if _, _, err := e.do(ctx, http.MethodGet, u, nil, nil); err != nil {
			return fmt.Errorf("%s login: %w", e.def.Name, err)
		}
	case "cookie":
		return fmt.Errorf("%s requires a session cookie — set the 'cookie' setting", e.def.Name)
	default:
		return fmt.Errorf("%s uses an unsupported login method %q", e.def.Name, l.Method)
	}

	e.loggedIn = true
	e.lastLoginAt = time.Now()
	logger.Debug("cardigann: logged in", "tracker", e.def.Name)
	return nil
}

// loginError reports the tracker's own error message when a login is refused.
func (e *Engine) loginError(doc *goquery.Document, raw string) string {
	for _, le := range e.def.Login.Error {
		sel := strings.TrimSpace(le.Selector)
		if sel == "" {
			continue
		}
		if s := doc.Find(sel); s.Length() > 0 {
			msg := strings.TrimSpace(s.First().Text())
			if msg == "" {
				msg = "login form reported an error"
			}
			return msg
		}
	}
	return ""
}

// Result is one release parsed from a tracker's search page.
type Result struct {
	Title       string
	Details     string
	Download    string
	Magnet      string
	InfoHash    string
	Size        int64
	Seeders     int
	Leechers    int
	Grabs       int
	PublishDate string
	Category    string
}

// Search runs the definition's search and returns the parsed rows.
func (e *Engine) Search(ctx context.Context, keywords string, categories []string, query map[string]string) ([]Result, error) {
	if err := e.Login(ctx); err != nil {
		return nil, err
	}
	base := e.newContext()
	base.Keywords = e.filterKeywords(keywords)
	base.Categories = categories
	if query != nil {
		base.Query = query
	}

	var out []Result
	paths := e.def.Search.Paths
	if len(paths) == 0 {
		paths = []SearchPath{{Path: ""}}
	}
	var firstErr error
	for _, p := range paths {
		rows, err := e.searchPath(ctx, p, base)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			logger.Debug("cardigann: search path failed", "tracker", e.def.Name, "path", p.Path, "err", err)
			continue
		}
		out = append(out, rows...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// filterKeywords applies the definition's keyword rewrites.
func (e *Engine) filterKeywords(kw string) string {
	if e.def.Search.Keywords == nil {
		return kw
	}
	return ApplyFilters(kw, e.def.Search.Keywords, e.newContext())
}

func (e *Engine) searchPath(ctx context.Context, p SearchPath, base *Context) ([]Result, error) {
	target := ResolveURL(e.baseURL, base.Expand(p.Path))

	// Inputs are merged: the search block's inputs first, then this path's.
	form := url.Values{}
	var rawExtra string
	merge := func(inputs map[string]string) {
		for k, v := range inputs {
			expanded := base.Expand(v)
			if k == "$raw" {
				rawExtra = strings.Trim(expanded, "&")
				continue
			}
			form.Set(k, expanded)
		}
	}
	merge(e.def.Search.Inputs)
	merge(p.Inputs)

	method := http.MethodGet
	if strings.EqualFold(strings.TrimSpace(p.Method), "post") {
		method = http.MethodPost
	}

	logger.Debug("cardigann: search request", "tracker", e.def.Name, "url", target)

	if method == http.MethodGet {
		q := form.Encode()
		if rawExtra != "" {
			if q != "" {
				q += "&"
			}
			q += rawExtra
		}
		if q != "" {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + q
		}
		form = nil
	}

	headers := headerStrings(e.def.Search.Headers)
	if p.Response != nil && strings.EqualFold(strings.TrimSpace(p.Response.Type), "json") {
		// A JSON API endpoint content-negotiates on the Accept header.
		// TorrentLeech's /torrents/browse/list/... returns the HTML browse
		// page when it sees Accept: text/html (the default) and JSON only
		// when it sees Accept: application/json. Prowlarr sends the latter;
		// without it SeedStream gets HTML, parseJSONRows returns 0, and the
		// search looks empty even with a valid logged-in session.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Accept"] = "application/json"
	}

	doc, raw, err := e.do(ctx, method, target, form, headers)
	if err != nil {
		return nil, err
	}
	logger.Debug("cardigann: search response", "tracker", e.def.Name, "url", target, "html_bytes", len(raw))
	if len(raw) > 0 {
		sample := string(raw)
		if len(sample) > 500 {
			sample = sample[:500]
		}
		logger.Debug("cardigann: search html sample", "tracker", e.def.Name, "html", sample)
	}
	if p.Response != nil && strings.EqualFold(strings.TrimSpace(p.Response.Type), "json") {
		return e.parseJSONRows([]byte(raw)), nil
	}
	return e.parseRows(doc), nil
}

// parseRows walks the result rows and extracts each configured field.
func (e *Engine) parseRows(doc *goquery.Document) []Result {
	sel := strings.TrimSpace(e.def.Search.Rows.Selector)
	if sel == "" {
		return nil
	}
	var out []Result
	after := e.def.Search.Rows.After
	doc.Find(sel).Each(func(i int, row *goquery.Selection) {
		if after > 0 && i < after {
			return
		}
		ctx := e.newContext()
		values := map[string]string{}
		for name, f := range e.def.Search.Fields {
			v := e.extractField(row, f, ctx)
			values[name] = v
			ctx.Result[name] = v
		}
		// A second pass lets fields reference earlier ones via .Result.
		for name, f := range e.def.Search.Fields {
			if strings.Contains(f.Text, "{{") {
				v := e.extractField(row, f, ctx)
				values[name] = v
				ctx.Result[name] = v
			}
		}
		if r, ok := e.toResult(values); ok {
			out = append(out, r)
		}
	})
	return out
}

// extractField resolves one field from a row.
func (e *Engine) extractField(row *goquery.Selection, f Field, ctx *Context) string {
	var value string
	switch {
	case strings.TrimSpace(f.Selector) != "":
		s := row.Find(f.Selector)
		if s.Length() == 0 {
			// Some definitions select the row element itself.
			if row.Is(f.Selector) {
				s = row
			} else {
				return ApplyFilters(ctx.Expand(f.Text), f.Filters, ctx)
			}
		}
		sel := s.First()
		if rm := strings.TrimSpace(f.Remove); rm != "" {
			sel = sel.Clone()
			sel.Find(rm).Remove()
		}
		if attr := strings.TrimSpace(f.Attribute); attr != "" {
			value, _ = sel.Attr(attr)
		} else {
			value = sel.Text()
		}
	case f.Text != "":
		value = ctx.Expand(f.Text)
	default:
		if attr := strings.TrimSpace(f.Attribute); attr != "" {
			value, _ = row.Attr(attr)
		} else {
			value = row.Text()
		}
	}
	value = strings.TrimSpace(value)
	return ApplyFilters(value, f.Filters, ctx)
}

// parseJSONRows parses a JSON search response the way Cardigann's HTML
// engine parses HTML: walk rows.selector as a JSON path (a top-level key
// like "torrentList", with or without a "$." prefix as count.selector uses),
// and for each object in the resulting list extract every configured field
// by its JSON key. goquery cannot find elements in a JSON document — it
// treats the whole body as one text node — so without this path a tracker
// that declares response.type: json returns zero rows silently, even when
// the response itself is correct. The Cardigann definitions of TorrentLeech
// and every Unit3d-API tracker are JSON-shaped.
//
// Field selectors are JSON keys: "fid", "seeders", "addedTimestamp". Nested
// keys are written with dots ("group.name"). Filters and the Text/template
// cross-reference pattern ({{ .Result._id }}) work the same way as in HTML
// mode because the surrounding machinery (ApplyFilters, ctx.Expand) is
// unchanged — only the extraction step reads from a parsed JSON object.
func (e *Engine) parseJSONRows(raw []byte) []Result {
	sel := strings.TrimSpace(e.def.Search.Rows.Selector)
	if sel == "" {
		return nil
	}
	path := jsonPath(sel)
	top, err := parseJSONObject(raw)
	if err != nil || top == nil {
		bodyPrefix := string(raw)
		if len(bodyPrefix) > 500 {
			bodyPrefix = bodyPrefix[:500]
		}
		logger.Warn("cardigann: JSON response did not parse as an object — this is the body SeedStream received", "base", e.baseURL, "rows_selector", sel, "err", err, "body_prefix", bodyPrefix)
		return nil
	}
	arr, ok := jsonNavigateArray(top, path)
	if !ok {
		bodyPrefix := string(raw)
		if len(bodyPrefix) > 500 {
			bodyPrefix = bodyPrefix[:500]
		}
		logger.Warn("cardigann: rows JSON path did not locate an array — the response parsed as JSON but the rows selector missed", "base", e.baseURL, "rows_selector", sel, "path", path, "body_prefix", bodyPrefix)
		return nil
	}
	var out []Result
	after := e.def.Search.Rows.After
	for i, obj := range arr {
		if after > 0 && i < after {
			continue
		}
		ctx := e.newContext()
		values := map[string]string{}
		for name, f := range e.def.Search.Fields {
			v := e.extractJSONField(obj, f, ctx)
			values[name] = v
			ctx.Result[name] = v
		}
		for name, f := range e.def.Search.Fields {
			if strings.Contains(f.Text, "{{") {
				v := e.extractJSONField(obj, f, ctx)
				values[name] = v
				ctx.Result[name] = v
			}
		}
		if r, ok := e.toResult(values); ok {
			out = append(out, r)
		}
	}
	return out
}

// extractJSONField resolves one field from a JSON row object, mirroring the
// HTML extractor's selector/text/attribute precedence. In JSON mode a
// selector is a key into the object (or a dotted path); text is a literal
// template (constants and {{ .Result.X }} cross-references); attribute is
// unused but tolerated to keep definitions portable.
func (e *Engine) extractJSONField(obj map[string]any, f Field, ctx *Context) string {
	var value string
	switch {
	case strings.TrimSpace(f.Selector) != "":
		if v, ok := jsonNavigate(obj, jsonPath(f.Selector)); ok {
			value = jsonScalarToString(v)
		} else if !f.Optional {
			// A mandatory field that is absent is an empty string; filters
			// and case maps run on it exactly as they would on an empty
			// HTML find.
			value = ""
		}
		// Case maps are how definitions like TorrentLeech's download_multiplier
		// translate "0" into "freeleech-based" values. They match against
		// the raw extracted scalar string, the same shape the HTML path
		// would have produced.
		if len(f.Case) > 0 {
			value = mapCase(f.Case, value)
		}
	case f.Text != "":
		value = ctx.Expand(f.Text)
	default:
		// No selector and no text: nothing to extract; fall through with
		// an empty value so filters still run.
		value = ""
	}
	value = strings.TrimSpace(value)
	return ApplyFilters(value, f.Filters, ctx)
}

// parseJSONObject unmarshals a JSON document into a tree of map[string]any
// and []any values. A non-object document (e.g. a bare array) returns an
// error because every Cardigann JSON definition's rows.selector points INTO
// an object top-level (torrentList, numFound, etc).
func parseJSONObject(raw []byte) (map[string]any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON response is not an object (got %T)", v)
	}
	return obj, nil
}

// jsonPath strips an optional leading "$." from a Cardigann selector so the
// same convention used for count.selector ("$.numFound") works for rows and
// fields too. The remainder is the dotted JSON path to navigate.
func jsonPath(sel string) []string {
	s := strings.TrimSpace(sel)
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimPrefix(s, ".")
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

// jsonNavigate walks a dotted JSON path through nested objects. Strings are
// coerced to scalar values via jsonScalarToString so callers always receive
// the same shape as a leaf value as they would from the HTML extractor.
func jsonNavigate(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, seg := range path {
		switch m := cur.(type) {
		case map[string]any:
			v, ok := m[seg]
			if !ok {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

// jsonNavigateArray navigates a path and asserts the result is a JSON array.
func jsonNavigateArray(obj map[string]any, path []string) ([]map[string]any, bool) {
	v, ok := jsonNavigate(obj, path)
	if !ok {
		return nil, false
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, row)
	}
	return out, true
}

// jsonScalarToString coerces a parsed JSON scalar (string, number, bool, null)
// to the same plain string the HTML extractor would produce. Numbers and
// bools follow Go's default formatting, matching what a definition author
// would write in a Cardigann selector (e.g. "0" for freeleech).
func jsonScalarToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// JSON numbers always come through as float64; print without a
		// trailing .0 when the value is integral, so "0" stays "0" and
		// "1.5" stays "1.5".
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	case map[string]any, []any:
		// A row field that points at a sub-object or array: stringify the
		// JSON as a fallback rather than emit "[map[X:%!s(...)" garbage.
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// mapCase applies a Cardigann field's case map to an extracted value. The
// YAML loader stores keys as strings (JSON keys are strings, Go map keys are
// strings), so "0" maps to the configured override and "*" is the wildcard.
// Returns the value unchanged when no case matched and there is no wildcard.
func mapCase(cases map[string]string, value string) string {
	if cases == nil {
		return value
	}
	if v, ok := cases[value]; ok {
		return v
	}
	if v, ok := cases["*"]; ok {
		return v
	}
	return value
}

// toResult converts extracted field values into a Result, dropping rows without
// a title or any way to fetch the torrent.
func (e *Engine) toResult(v map[string]string) (Result, bool) {
	title := strings.TrimSpace(v["title"])
	if title == "" {
		return Result{}, false
	}
	r := Result{
		Title:       title,
		Details:     ResolveURL(e.baseURL, v["details"]),
		InfoHash:    strings.TrimSpace(v["infohash"]),
		PublishDate: strings.TrimSpace(v["date"]),
		Category:    strings.TrimSpace(v["category"]),
		Size:        ParseSize(v["size"]),
	}
	if d := strings.TrimSpace(v["download"]); d != "" {
		if strings.HasPrefix(d, "magnet:") {
			r.Magnet = d
		} else {
			r.Download = ResolveURL(e.baseURL, d)
		}
	}
	if m := strings.TrimSpace(v["magnet"]); m != "" {
		r.Magnet = m
	}
	r.Seeders = atoi(v["seeders"])
	r.Leechers = atoi(v["leechers"])
	r.Grabs = atoi(v["grabs"])
	if r.Download == "" && r.Magnet == "" && r.InfoHash == "" {
		return Result{}, false
	}
	return r, true
}

func atoi(s string) int {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		// Values sometimes arrive as "1.2K" or with trailing text.
		if f, ferr := strconv.ParseFloat(strings.TrimRight(s, "KkMm"), 64); ferr == nil {
			switch {
			case strings.HasSuffix(strings.ToUpper(s), "K"):
				return int(f * 1000)
			case strings.HasSuffix(strings.ToUpper(s), "M"):
				return int(f * 1000000)
			}
			return int(f)
		}
		return 0
	}
	return n
}

// headerStrings flattens definition headers for the HTTP layer, which takes
// plain strings.
func headerStrings(in map[string]HeaderValue) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}
