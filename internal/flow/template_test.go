package flow

import (
	"reflect"
	"testing"
)

// renderOptions must template values INSIDE a list, so a verb step like
//
//	uses: gh.rerequest_review
//	options: { reviewers: ["{{.author}}"] }
//
// resolves to the reviewer login. Guards the pattern the changes_requested
// re-request step depends on (the retired rerequest_review: field's replacement).
func TestRenderOptionsTemplatesListElements(t *testing.T) {
	data := map[string]any{"author": "octocat", "repo": "o/r", "pr": 7}
	opts := map[string]any{
		"repo":      "{{.repo}}",
		"pr":        "{{.pr}}",
		"reviewers": []any{"{{.author}}"},
	}
	got, err := renderOptions(opts, data)
	if err != nil {
		t.Fatalf("renderOptions error: %v", err)
	}
	if !reflect.DeepEqual(got["reviewers"], []any{"octocat"}) {
		t.Fatalf("reviewers = %#v, want [octocat]", got["reviewers"])
	}
	// A sole {{.pr}} ref keeps its underlying int type (soleFieldRef path).
	if got["pr"] != 7 {
		t.Fatalf("pr = %#v, want int 7", got["pr"])
	}
}

func TestTemplateDefaultCoalesce(t *testing.T) {
	data := map[string]any{"sev": "high", "empty": "", "n": 0}
	cases := []struct {
		tmpl, want string
	}{
		// Pipe and call forms.
		{`{{.sev | default "low"}}`, "high"},
		{`{{.missing | default "low"}}`, "low"},
		{`{{default "low" .missing}}`, "low"},
		{`{{.empty | default "low"}}`, "low"},
		// 0 is a real value.
		{`{{.n | default 5}}`, "0"},
		// coalesce: first present, non-empty.
		{`{{coalesce .missing .empty .sev}}`, "high"},
		{`{{coalesce .missing .empty}}`, ""},
		{`{{coalesce .missing "z"}}`, "z"},
	}
	for _, c := range cases {
		got, err := render(c.tmpl, data)
		if err != nil {
			t.Fatalf("render(%q) error: %v", c.tmpl, err)
		}
		if got != c.want {
			t.Errorf("render(%q) = %q, want %q", c.tmpl, got, c.want)
		}
	}
}

// TestTemplateRefsSeeThroughFuncs proves validation still collects the field
// references inside default/coalesce calls (and that the calls parse).
func TestTemplateRefsSeeThroughFuncs(t *testing.T) {
	refs, err := templateRefs(`{{.a.b | default "x"}} {{coalesce .c "y"}}`)
	if err != nil {
		t.Fatalf("templateRefs error: %v", err)
	}
	want := map[string]bool{"a.b": true, "c": true}
	for _, r := range refs {
		delete(want, r)
	}
	if len(want) != 0 {
		t.Fatalf("refs %v missing from %v", want, refs)
	}
}
