package cardigann

import (
	"context"
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

	"seedstream/pkg/core/logger"
)

// Engine drives one tracker: it holds the definition, the operator's
// credentials, and the logged-in session.
type Engine struct {
	def     *Definition
	baseURL string
	config  map[string]string

	http *http.Client

	mu          sync.Mutex
	loggedIn    bool
	lastLoginAt time.Time
}

// loginTTL bounds how long a session is trusted before it is re-tested, so an
// expired cookie is noticed without re-logging in on every search.
const loginTTL = 30 * time.Minute

// NewEngine builds an engine for a definition. baseURLOverride lets the operator
// point at a different domain when a tracker moves, without editing the file.
func NewEngine(def *Definition, baseURLOverride string, settings map[string]string, timeout time.Duration) (*Engine, error) {
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
	return &Engine{
		def:     def,
		baseURL: base,
		config:  cfg,
		http:    &http.Client{Jar: jar, Timeout: timeout},
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
	var body io.Reader
	if form != nil && method == http.MethodPost {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if form != nil && method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	return ApplyFilters(kw, e.def.Search.Keywords.Filters, e.newContext())
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

	doc, _, err := e.do(ctx, method, target, form, e.def.Search.Headers)
	if err != nil {
		return nil, err
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
