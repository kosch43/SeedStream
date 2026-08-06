// Package cardigann implements the tracker-definition engine used by Jackett and
// Prowlarr, so SeedStream can talk to a private tracker's website directly
// instead of requiring one of those services in front of it.
//
// A definition is a YAML file describing one tracker: where it lives, how to log
// in, how to run a search, and which parts of the resulting page hold the title,
// size, seeders and download link. The engine reads that description and drives
// the site, which is why a single implementation can support hundreds of
// trackers without any tracker-specific code.
//
// Definitions are data, not code: they are loaded from disk at startup, so the
// published Jackett/Prowlarr definition set can be dropped in and updated
// without rebuilding SeedStream.
package cardigann

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Definition is one tracker description. The YAML tags mirror the Cardigann
// schema that Jackett and Prowlarr definitions are written in, so their files
// load unmodified.
type Definition struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Language    string   `yaml:"language"`
	Type        string   `yaml:"type"` // public, semi-private, private
	Encoding    string   `yaml:"encoding"`
	Links       []string `yaml:"links"`
	LegacyLinks []string `yaml:"legacylinks"`

	Caps     Caps      `yaml:"caps"`
	Settings []Setting `yaml:"settings"`
	Login    *Login    `yaml:"login"`
	Search   Search    `yaml:"search"`
	Download *Download `yaml:"download"`

	// sourceIsUser records whether this definition came from the operator's
	// definitions directory rather than the bundled set, so the UI can show
	// where a tracker's description came from.
	sourceIsUser bool `yaml:"-"`
}

// Caps declares which search modes a tracker supports and how its own category
// numbers map onto the standard Newznab ones SeedStream searches by.
type Caps struct {
	CategoryMappings []CategoryMapping   `yaml:"categorymappings"`
	Categories       map[string]string   `yaml:"categories"`
	Modes            map[string][]string `yaml:"modes"`
}

// CategoryMapping ties a tracker's own category id to a Newznab category name.
type CategoryMapping struct {
	ID   string `yaml:"id"`
	Cat  string `yaml:"cat"`
	Desc string `yaml:"desc"`
}

// Setting is one credential or option the user must supply, such as a username,
// password or passkey. The UI renders a field per setting.
type Setting struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"` // text, password, checkbox, select, info
	Label    string   `yaml:"label"`
	Default  string   `yaml:"default"`
	Options  []string `yaml:"options"`
	Required *bool    `yaml:"required"`
}

// IsCredential reports whether this setting is something the user actually types
// rather than an informational note rendered in the UI.
func (s Setting) IsCredential() bool {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "", "info":
		return false
	}
	return true
}

// IsSecret reports whether the value must be masked in the UI and redacted from
// API responses.
func (s Setting) IsSecret() bool {
	t := strings.ToLower(strings.TrimSpace(s.Type))
	if t == "password" {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(s.Name))
	return strings.Contains(n, "passkey") || strings.Contains(n, "apikey") ||
		strings.Contains(n, "api_key") || strings.Contains(n, "token") ||
		strings.Contains(n, "cookie") || strings.Contains(n, "rsskey")
}

// Login describes how to authenticate. Method is one of "form", "post", "get",
// "cookie" or "none"; an absent Login block means the tracker needs no login.
type Login struct {
	Path         string            `yaml:"path"`
	Method       string            `yaml:"method"`
	Form         string            `yaml:"form"`
	SubmitPath   string            `yaml:"submitpath"`
	Inputs       map[string]string `yaml:"inputs"`
	SelectorInpt map[string]Field  `yaml:"selectorinputs"`
	Error        []LoginError      `yaml:"error"`
	Test         *LoginTest        `yaml:"test"`
	Captcha      *Captcha          `yaml:"captcha"`
}

// LoginError is a selector whose presence means the login failed.
type LoginError struct {
	Path     string `yaml:"path"`
	Selector string `yaml:"selector"`
	Message  Field  `yaml:"message"`
}

// LoginTest is a cheap request used to check whether an existing session is
// still valid, avoiding a login on every search.
type LoginTest struct {
	Path     string `yaml:"path"`
	Selector string `yaml:"selector"`
}

// Captcha marks a tracker that challenges logins. SeedStream cannot solve these,
// so such definitions are reported as needing a cookie instead.
type Captcha struct {
	Type     string `yaml:"type"`
	Selector string `yaml:"selector"`
	Input    string `yaml:"input"`
}

// Search describes how to run a query and where the results are on the page.
type Search struct {
	Paths     []SearchPath      `yaml:"paths"`
	Headers   map[string]string `yaml:"headers"`
	Inputs    map[string]string `yaml:"inputs"`
	Keywords  *KeywordFilters   `yaml:"keywordsfilters"`
	Rows      Rows              `yaml:"rows"`
	Fields    map[string]Field  `yaml:"fields"`
	Error     []LoginError      `yaml:"error"`
	Preprocss []Filter          `yaml:"preprocessingfilters"`
}

// SearchPath is one request the search makes. Definitions may list several when
// a tracker splits results across endpoints.
type SearchPath struct {
	Path       string            `yaml:"path"`
	Method     string            `yaml:"method"`
	Inputs     map[string]string `yaml:"inputs"`
	Response   *Response         `yaml:"response"`
	Categories []string          `yaml:"categories"`
}

// Response declares a non-HTML result format, currently JSON.
type Response struct {
	Type      string `yaml:"type"`
	NoResults string `yaml:"noResultsMessage"`
	Attribute string `yaml:"attribute"`
}

// KeywordFilters rewrite the user's query before it is sent, e.g. stripping
// characters a tracker's search cannot handle.
type KeywordFilters struct {
	Filters []Filter `yaml:"filters"`
}

// Rows locates the repeating result element on the page.
type Rows struct {
	Selector string   `yaml:"selector"`
	After    int      `yaml:"after"`
	Remove   string   `yaml:"remove"`
	DateHead string   `yaml:"dateheaders"`
	Filters  []Filter `yaml:"filters"`
	Count    Field    `yaml:"count"`
}

// Field extracts one value from a result row: pick an element with Selector,
// take its text or an Attribute, then run Filters over the result. Text
// provides a constant or template when there is nothing to select.
type Field struct {
	Selector  string            `yaml:"selector"`
	Attribute string            `yaml:"attribute"`
	Remove    string            `yaml:"remove"`
	Case      map[string]string `yaml:"case"`
	Text      string            `yaml:"text"`
	Filters   []Filter          `yaml:"filters"`
	Optional  bool              `yaml:"optional"`
}

// Filter is one transformation step applied to an extracted value. Args is left
// as a raw node because Cardigann filters take a string, a list or a map
// depending on the filter.
type Filter struct {
	Name string    `yaml:"name"`
	Args yaml.Node `yaml:"args"`
}

// Download describes how to turn a result into a .torrent, when the link on the
// page is not directly downloadable.
type Download struct {
	Selectors []Field `yaml:"selectors"`
	Before    *Field  `yaml:"before"`
	Infohash  *struct {
		Hash  Field `yaml:"hash"`
		Title Field `yaml:"title"`
	} `yaml:"infohash"`
}

// Parse reads a definition from YAML and checks the parts the engine depends on.
func Parse(data []byte) (*Definition, error) {
	var d Definition
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse definition: %w", err)
	}
	if strings.TrimSpace(d.ID) == "" {
		return nil, fmt.Errorf("definition has no id")
	}
	if strings.TrimSpace(d.Name) == "" {
		d.Name = d.ID
	}
	if len(d.Links) == 0 {
		return nil, fmt.Errorf("definition %q has no links", d.ID)
	}
	if strings.TrimSpace(d.Search.Rows.Selector) == "" && !d.isJSON() {
		return nil, fmt.Errorf("definition %q has no search row selector", d.ID)
	}
	return &d, nil
}

// isJSON reports whether the search returns JSON rather than HTML.
func (d *Definition) isJSON() bool {
	for _, p := range d.Search.Paths {
		if p.Response != nil && strings.EqualFold(p.Response.Type, "json") {
			return true
		}
	}
	return false
}

// BaseURL returns the site address to use: the operator's override when set,
// otherwise the definition's first published link. Trackers change domains, so
// the override is what keeps a definition usable without editing its file.
func (d *Definition) BaseURL(override string) string {
	if o := strings.TrimSpace(override); o != "" {
		return strings.TrimRight(o, "/") + "/"
	}
	if len(d.Links) > 0 {
		return strings.TrimRight(strings.TrimSpace(d.Links[0]), "/") + "/"
	}
	return ""
}

// Credentials returns only the settings the user must fill in.
func (d *Definition) Credentials() []Setting {
	var out []Setting
	for _, s := range d.Settings {
		if s.IsCredential() {
			out = append(out, s)
		}
	}
	return out
}

// RequiresLogin reports whether the tracker needs authentication at all.
func (d *Definition) RequiresLogin() bool {
	return d.Login != nil && !strings.EqualFold(strings.TrimSpace(d.Login.Method), "none")
}

// NeedsCaptcha reports whether logging in would require solving a challenge,
// which the engine cannot do. Such trackers need a pasted session cookie.
func (d *Definition) NeedsCaptcha() bool {
	return d.Login != nil && d.Login.Captcha != nil
}
