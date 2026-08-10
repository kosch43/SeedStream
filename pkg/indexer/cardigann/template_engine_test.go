package cardigann

import (
	"strings"
	"testing"
)

// TestRealDefinitionPathRenders is the field failure. TorrentLeech's search
// path uses else, or, and and — none of which the regex engine matched — so the
// unrendered template was URL-encoded into the request, the tracker rejected it,
// and every search returned nothing while login looked perfectly healthy.
func TestRealDefinitionPathRenders(t *testing.T) {
	cat := NewCatalog("")
	def, ok := cat.Get("torrentleech")
	if !ok {
		t.Skip("torrentleech definition not bundled")
	}
	ctx := &Context{
		Config:     map[string]string{"freeleech": "false", "exclude_scene": "false", "sort": "added", "type": "desc"},
		Keywords:   "rick and morty",
		Categories: []string{"26", "32"},
	}
	got := ctx.Expand(def.Search.Paths[0].Path)

	if strings.Contains(got, "{{") {
		t.Fatalf("template syntax survived into the request path: %q", got)
	}
	for _, want := range []string{"/categories/26,32", "/query/rick and morty", "/orderby/added"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in the rendered path, got %q", want, got)
		}
	}
	// The freeleech/nonscene facet is off, so it must not appear.
	if strings.Contains(got, "facets") {
		t.Errorf("neither filter is enabled, so no facets segment belongs here: %q", got)
	}
}

// TestCheckboxFalseIsFalsey is the trap in swapping engines. A checkbox is
// stored as the string "false", and to Go's template package any non-empty
// string is true — so without normalising, a filter the operator explicitly
// turned off would be applied to every search.
func TestCheckboxFalseIsFalsey(t *testing.T) {
	ctx := &Context{Config: map[string]string{"on": "true", "off": "false", "blank": ""}}

	if got := ctx.Expand(`{{ if .Config.off }}APPLIED{{ end }}`); got != "" {
		t.Errorf("a checkbox set to false must not fire, got %q", got)
	}
	if got := ctx.Expand(`{{ if .Config.blank }}APPLIED{{ end }}`); got != "" {
		t.Errorf("an empty setting must not fire, got %q", got)
	}
	if got := ctx.Expand(`{{ if .Config.on }}APPLIED{{ end }}`); got != "APPLIED" {
		t.Errorf("a checkbox set to true must fire, got %q", got)
	}
}

// TestFormsTheRegexEngineCouldNotHandle: the three that silently passed through.
func TestFormsTheRegexEngineCouldNotHandle(t *testing.T) {
	ctx := &Context{Config: map[string]string{"a": "true", "b": "false"}}

	cases := []struct{ tmpl, want string }{
		{`{{ if .Config.a }}yes{{ else }}no{{ end }}`, "yes"},
		{`{{ if .Config.b }}yes{{ else }}no{{ end }}`, "no"},
		{`{{ if or .Config.a .Config.b }}any{{ end }}`, "any"},
		{`{{ if and .Config.a .Config.b }}both{{ end }}`, ""},
		{`{{ if and .Config.a .Config.a }}both{{ end }}`, "both"},
	}
	for _, tc := range cases {
		if got := ctx.Expand(tc.tmpl); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

// TestExistingFormsStillWork guards the forms the old engine did handle, since
// every definition depends on them.
func TestExistingFormsStillWork(t *testing.T) {
	ctx := &Context{
		Config:     map[string]string{"username": "alice"},
		Query:      map[string]string{"season": "3"},
		Keywords:   "some movie",
		Categories: []string{"1", "2"},
		BaseURL:    "https://tracker.example",
	}
	cases := []struct{ tmpl, want string }{
		{`{{ .Config.username }}`, "alice"},
		{`{{ .Keywords }}`, "some movie"},
		{`{{ .Query.season }}`, "3"},
		{`{{ .BaseUrl }}`, "https://tracker.example"},
		{`{{ join .Categories "," }}`, "1,2"},
		{`{{ range .Categories }}cat[]={{.}}&{{ end }}`, "cat[]=1&cat[]=2&"},
		{`{{ if .Query.season }}s{{ .Query.season }}{{ end }}`, "s3"},
		{`no template here`, "no template here"},
	}
	for _, tc := range cases {
		if got := ctx.Expand(tc.tmpl); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

// TestEveryBundledDefinitionRenders is the breadth check. Every definition
// whose template is well-formed must render completely — a leftover "{{" would
// be URL-encoded into the request and rejected by the tracker, which is exactly
// how TorrentLeech failed while looking healthy.
//
// Seven bundled definitions carry upstream syntax errors (a stray parenthesis
// in "(eq .Config.disablesort .False))") and cannot be rendered by any engine.
// Those are excluded by parseability, not by name, so a future engine gap
// cannot hide behind the exemption.
func TestEveryBundledDefinitionRenders(t *testing.T) {
	cat := NewCatalog("")
	ctx := &Context{
		Config:     map[string]string{"sort": "added", "type": "desc"},
		Keywords:   "test",
		Categories: []string{"1"},
		BaseURL:    "https://x.invalid",
	}
	var leaked []string
	for _, entry := range cat.Search("", 1000) {
		def, ok := cat.Get(entry.ID)
		if !ok {
			continue
		}
		for _, p := range def.Search.Paths {
			if !ctx.parses(p.Path) {
				continue // malformed upstream; no engine can render it
			}
			if strings.Contains(ctx.Expand(p.Path), "{{") {
				leaked = append(leaked, entry.ID)
				break
			}
		}
	}
	if len(leaked) > 0 {
		t.Fatalf("%d definitions still emit template syntax into their request path: %v",
			len(leaked), leaked[:min(len(leaked), 10)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
