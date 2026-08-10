package cardigann

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"seedstream/pkg/core/logger"
)

// Context carries everything a definition's templates can refer to while a
// search runs: the user's credentials, the query, and values already extracted
// from the current row.
type Context struct {
	Config     map[string]string
	Keywords   string
	Query      map[string]string
	Categories []string
	Result     map[string]string
	BaseURL    string
	Today      time.Time
}

// templateVar matches the {{ .Something }} and {{ range ... }} forms Cardigann
// definitions use. The engine implements the small subset the definitions
// actually rely on rather than pulling in a full template language, so that a
// malformed definition can never execute arbitrary logic.

// Expand renders a definition's template against ctx.
//
// This is Go's text/template rather than a set of regexes. The regex engine it
// replaces matched only "{{ if .Something }}…{{ end }}", so three forms real
// definitions use went straight through untouched: "{{ else }}", "{{ if or … }}"
// and "{{ if and … }}". TorrentLeech's search path uses all three, so its
// unrendered template was URL-encoded into the request path, the tracker
// rejected the nonsense URL, and every search returned nothing while login and
// everything else looked healthy.
//
// text/template is the same class of engine Jackett and Prowlarr use, so the
// definitions behave as their authors tested them.
func (ctx *Context) Expand(tmpl string) string {
	if tmpl == "" || !strings.Contains(tmpl, "{{") {
		return tmpl
	}
	t, err := template.New("cardigann").Funcs(ctx.funcMap()).Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		logger.Debug("cardigann: template parse failed, using it literally",
			"template", tmpl, "err", err)
		return tmpl
	}
	var b strings.Builder
	if err := t.Execute(&b, ctx.data()); err != nil {
		logger.Debug("cardigann: template execution failed, using it literally",
			"template", tmpl, "err", err)
		return tmpl
	}
	return b.String()
}

// parses reports whether a template is well-formed. A definition carrying a
// syntax error cannot be rendered by any engine, and separating that from an
// engine gap is what keeps the two from being confused.
func (ctx *Context) parses(tmpl string) bool {
	if !strings.Contains(tmpl, "{{") {
		return true
	}
	_, err := template.New("cardigann").Funcs(ctx.funcMap()).Parse(tmpl)
	return err == nil
}

// funcMap provides the helpers Cardigann definitions call.
func (ctx *Context) funcMap() template.FuncMap {
	return template.FuncMap{
		// join is Cardigann's own spelling: {{ join .Categories "," }}. Go's
		// built-in has the arguments the other way round, so it cannot be used
		// directly.
		"join": func(items []string, sep string) string { return strings.Join(items, sep) },
	}
}

// data is what the template sees.
//
// Config values are normalised first, because Go and Cardigann disagree about
// truth. A checkbox setting is stored as the string "false", and to Go's
// template package a non-empty string is true — so "{{ if .Config.freeleech }}"
// would fire on a checkbox the operator had explicitly turned off. Emptying
// those makes Go's truthiness mean what the definition intends.
func (ctx *Context) data() map[string]any {
	cfg := make(map[string]string, len(ctx.Config))
	for k, v := range ctx.Config {
		if isFalsey(v) {
			cfg[k] = ""
			continue
		}
		cfg[k] = v
	}
	return map[string]any{
		"Config":     cfg,
		"Query":      ctx.Query,
		"Result":     ctx.Result,
		"Keywords":   ctx.Keywords,
		"Categories": ctx.Categories,
		"BaseUrl":    ctx.BaseURL,
		"BaseURL":    ctx.BaseURL,
		"Today":      ctx.Today.Format("2006-01-02"),
		// Cardigann's own boolean literals, used as {{ eq .Config.x .False }}.
		// False is the empty string rather than "false" so it compares equal to
		// a normalised off-setting: the same normalisation that makes "if" work
		// would otherwise make every "eq …​ .False" answer the wrong way.
		"True":  "true",
		"False": "",
	}
}

// isFalsey reports whether a stored setting means "off". Checkboxes arrive as
// strings, so this is the only place the distinction can be made.
func isFalsey(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false", "0", "no", "off":
		return true
	}
	return false
}

// lookup resolves a dotted template path such as "Config.username".
func (ctx *Context) lookup(path string) string {
	parts := strings.SplitN(path, ".", 2)
	switch parts[0] {
	case "Keywords":
		return ctx.Keywords
	case "BaseUrl", "BaseURL":
		return ctx.BaseURL
	case "Today":
		return ctx.Today.Format("2006-01-02")
	case "Config":
		if len(parts) == 2 {
			return ctx.Config[parts[1]]
		}
	case "Query":
		if len(parts) == 2 {
			return ctx.Query[parts[1]]
		}
	case "Result":
		if len(parts) == 2 {
			return ctx.Result[parts[1]]
		}
	}
	return ""
}

// argStrings flattens a filter's YAML args into a string slice, since Cardigann
// filters take a bare string, a list, or a nested list depending on the filter.
func argStrings(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			out = append(out, argStrings(*c)...)
		}
		return out
	}
	return nil
}

// ApplyFilters runs a definition's filter chain over an extracted value.
// An unknown filter leaves the value untouched rather than failing the whole
// search, so one unsupported step cannot take a tracker offline.
func ApplyFilters(value string, filters []Filter, ctx *Context) string {
	for _, f := range filters {
		args := argStrings(f.Args)
		switch strings.ToLower(strings.TrimSpace(f.Name)) {
		case "querystring":
			value = queryStringValue(value, arg(args, 0))
		case "replace":
			if len(args) >= 2 {
				value = strings.ReplaceAll(value, args[0], args[1])
			}
		case "re_replace", "regexp_replace":
			if len(args) >= 2 {
				if re, err := regexp.Compile(args[0]); err == nil {
					value = re.ReplaceAllString(value, args[1])
				}
			}
		case "regexp":
			if len(args) >= 1 {
				if re, err := regexp.Compile(args[0]); err == nil {
					if m := re.FindStringSubmatch(value); len(m) > 1 {
						value = m[1]
					} else if len(m) == 1 {
						value = m[0]
					} else {
						value = ""
					}
				}
			}
		case "split":
			if len(args) >= 2 {
				idx, _ := strconv.Atoi(args[1])
				parts := strings.Split(value, args[0])
				if idx < 0 {
					idx += len(parts)
				}
				if idx >= 0 && idx < len(parts) {
					value = parts[idx]
				} else {
					value = ""
				}
			}
		case "trim":
			if len(args) >= 1 && args[0] != "" {
				value = strings.Trim(value, args[0])
			} else {
				value = strings.TrimSpace(value)
			}
		case "prepend":
			value = arg(args, 0) + value
		case "append":
			value = value + arg(args, 0)
		case "tolower":
			value = strings.ToLower(value)
		case "toupper":
			value = strings.ToUpper(value)
		case "urldecode":
			if u, err := url.QueryUnescape(value); err == nil {
				value = u
			}
		case "urlencode":
			value = url.QueryEscape(value)
		case "htmldecode":
			value = html.UnescapeString(value)
		case "timeparse", "dateparse", "fuzzytime":
			if t, ok := parseTime(value, args); ok {
				value = t.UTC().Format(time.RFC1123Z)
			}
		case "timeago", "reltime":
			if t, ok := parseRelativeTime(value, ctx.Today); ok {
				value = t.UTC().Format(time.RFC1123Z)
			}
		case "validate", "dump", "strdump", "hexdump":
			// diagnostic-only in Cardigann; nothing to do here
		}
	}
	return value
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// queryStringValue pulls one parameter out of a URL or query fragment, which is
// how definitions turn a details link into a torrent id.
func queryStringValue(value, key string) string {
	if key == "" {
		return value
	}
	if i := strings.Index(value, "?"); i >= 0 {
		value = value[i+1:]
	}
	q, err := url.ParseQuery(value)
	if err != nil {
		return ""
	}
	return q.Get(key)
}

// timeLayouts are tried in order when a definition does not name one, covering
// the formats trackers commonly print.
var timeLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC3339,
	"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	"02-01-2006 15:04:05", "02-01-2006 15:04", "02-01-2006",
	"01-02-2006 15:04:05", "2006/01/02 15:04:05", "2006/01/02",
	"Jan 2 2006, 15:04", "Jan 2 2006", "02 Jan 2006 15:04:05", "02 Jan 2006",
	"2 January 2006 15:04:05", "2 January 2006",
}

func parseTime(value string, args []string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return time.Time{}, false
	}
	layouts := timeLayouts
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		layouts = append([]string{args[0]}, timeLayouts...)
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var reRelTime = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(sec|second|min|minute|hour|hr|day|week|month|year)s?`)

// parseRelativeTime turns "3 days ago" and similar into an absolute time, which
// is how many trackers print upload dates.
func parseRelativeTime(value string, now time.Time) (time.Time, bool) {
	if now.IsZero() {
		now = time.Now()
	}
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.HasPrefix(lower, "today"):
		return now, true
	case strings.HasPrefix(lower, "yesterday"):
		return now.AddDate(0, 0, -1), true
	}
	matches := reRelTime.FindAllStringSubmatch(lower, -1)
	if len(matches) == 0 {
		return time.Time{}, false
	}
	out := now
	for _, m := range matches {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		switch m[2] {
		case "sec", "second":
			out = out.Add(-time.Duration(n) * time.Second)
		case "min", "minute":
			out = out.Add(-time.Duration(n) * time.Minute)
		case "hour", "hr":
			out = out.Add(-time.Duration(n) * time.Hour)
		case "day":
			out = out.AddDate(0, 0, -int(n))
		case "week":
			out = out.AddDate(0, 0, -int(n)*7)
		case "month":
			out = out.AddDate(0, -int(n), 0)
		case "year":
			out = out.AddDate(-int(n), 0, 0)
		}
	}
	return out, true
}

// ParseSize converts the human-readable sizes trackers print into bytes.
func ParseSize(value string) int64 {
	v := strings.TrimSpace(strings.ToUpper(value))
	v = strings.ReplaceAll(v, ",", "")
	v = strings.ReplaceAll(v, " ", " ")
	m := regexp.MustCompile(`([0-9]*\.?[0-9]+)\s*([KMGTP]?I?B)`).FindStringSubmatch(v)
	if len(m) < 3 {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	unit := m[2]
	mult := float64(1)
	switch {
	case strings.HasPrefix(unit, "K"):
		mult = 1 << 10
	case strings.HasPrefix(unit, "M"):
		mult = 1 << 20
	case strings.HasPrefix(unit, "G"):
		mult = 1 << 30
	case strings.HasPrefix(unit, "T"):
		mult = 1 << 40
	case strings.HasPrefix(unit, "P"):
		mult = 1 << 50
	}
	return int64(n * mult)
}

// ResolveURL turns a link found on a page into an absolute one.
func ResolveURL(base, href string) string {
	h := strings.TrimSpace(href)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "magnet:") || strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		return h
	}
	b, err := url.Parse(base)
	if err != nil {
		return h
	}
	r, err := url.Parse(h)
	if err != nil {
		return h
	}
	return b.ResolveReference(r).String()
}

var _ = fmt.Sprintf
