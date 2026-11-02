package memory

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

type scopeKeyVectors struct {
	Facts map[string]any `json:"facts"`
	Cases []struct {
		Template string `json:"template"`
		Want     string `json:"want"`
		Drop     bool   `json:"drop"`
	} `json:"cases"`
}

// loadScopeKeyVectors reads the shared vectors, with whole JSON numbers as
// ints — the shape a run's facts carry.
func loadScopeKeyVectors(t *testing.T) scopeKeyVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/scope_key_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v scopeKeyVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	v.Facts = intsOf(v.Facts).(map[string]any)
	return v
}

func intsOf(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = intsOf(e)
		}
	case []any:
		for i, e := range x {
			x[i] = intsOf(e)
		}
	case float64:
		if x == float64(int(x)) {
			return int(x)
		}
	}
	return v
}

func TestScopeKeyVectors(t *testing.T) {
	v := loadScopeKeyVectors(t)
	if len(v.Cases) < 30 {
		t.Fatalf("vectors: %d cases", len(v.Cases))
	}
	for _, c := range v.Cases {
		got, err := RenderScopeKey(c.Template, v.Facts)
		switch {
		case c.Drop && err == nil:
			t.Errorf("%q must not render, got %q", c.Template, got)
		case !c.Drop && err != nil:
			t.Errorf("%q: %v", c.Template, err)
		case !c.Drop && got != c.Want:
			t.Errorf("%q = %q, want %q", c.Template, got, c.Want)
		}
	}
}

// What a key may not do is refused at load (CheckScopeKey), not only at
// render.
func TestCheckScopeKey(t *testing.T) {
	for _, ok := range []string{"acme/api", "", "team:{{.inputs.team}}", "${repo}", "{{.x | default \"y\"}}", "{{index .labels 0}}"} {
		if err := CheckScopeKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"{{if .x}}y{{end}}", "{{range .l}}{{end}}", "{{with .x}}{{end}}", "{{define \"t\"}}x{{end}}",
		"{{template \"t\"}}", "{{$v := .x}}", "{{kv \"a\" \"b\" \"c\"}}", "{{vault \"a\" \"b\"}}", "{{secret \"x\"}}",
		"{{memory \"global\" 1}}", "{{call .f}}", "{{len .x}}", "{{.x", "a\nb", "a\x00b", "tab\there"} {
		if err := CheckScopeKey(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// A rendered value is data: it is never rendered again, and a control
// character a fact carries refuses the key.
func TestRenderedFactsStayLiteral(t *testing.T) {
	facts := map[string]any{"title": "{{.secret}}", "x": "${repo}", "nl": "a\nb", "secret": "s3cr3t", "repo": "acme/api"}
	if got, err := RenderScopeKey("t:{{.title}}", facts); err != nil || got != "t:{{.secret}}" {
		t.Fatalf("a fact's template text must stay text: %q %v", got, err)
	}
	if got, err := RenderScopeKey("t:{{.x}}", facts); err != nil || got != "t:${repo}" {
		t.Fatalf("a fact's alias text must stay text: %q %v", got, err)
	}
	if _, err := RenderScopeKey("t:{{.nl}}", facts); err == nil {
		t.Fatal("a control character from a fact must refuse the key")
	}
}

// The compat aliases are exactly the three template references.
func TestScopeKeyAliases(t *testing.T) {
	f := func(repo, wf, step string) map[string]any {
		return map[string]any{"repo": repo, "workflow": wf, "step": step}
	}
	for _, tc := range []struct {
		in    []string
		facts map[string]any
		want  []string
	}{
		{[]string{"standard:${repo}", "emphasis:${repo}"}, f("acme/api", "", ""), []string{"standard:acme/api", "emphasis:acme/api"}},
		{[]string{"${workflow}/${step}@${repo}"}, f("acme/api", "nightly", "lint"), []string{"nightly/lint@acme/api"}},
		{[]string{"standard:${repo}", "global-ish"}, f("", "w", "s"), []string{"global-ish"}},
		{[]string{"${workflow}:${repo}"}, f("", "w", "s"), []string{}},
		{[]string{"${repo}", "${step}", "literal", ""}, f("acme/api", "", "lint"), []string{"acme/api", "lint", "literal", ""}},
		{[]string{" spaced ", "${unknown}"}, f("r", "w", "s"), []string{" spaced ", "${unknown}"}},
		{[]string{"x:${repo}"}, f("${step}", "w", "lint"), []string{"x:${step}"}},
	} {
		if got := RenderScopeKeys(tc.in, tc.facts); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, a := range []string{"${repo}", "${workflow}", "${step}", "x{{.y}}"} {
		if !IsScopeTemplate(a) {
			t.Errorf("%q is a template", a)
		}
	}
	for _, a := range []string{"acme/api", "${REPO}", "${unknown}", "{scope}"} {
		if IsScopeTemplate(a) {
			t.Errorf("%q is not a template", a)
		}
	}
}

func TestScopeFacts(t *testing.T) {
	trusted := core.Trigger{Kind: "review_requested", Title: "t", TargetTrusted: true,
		Target:  core.Target{Repo: "acme/api", Owner: "acme", Name: "api", Number: 7, PR: 7, HeadSHA: "abc", BaseRef: "main", HTMLURL: "u"},
		Context: map[string]any{"author": "octo", "repo": "context/wins-not", "labels": []string{"x"}}}
	data := map[string]any{"inputs": map[string]any{"team": "payments"}, "secrets": map[string]any{"gh": "tok"},
		"vaults": map[string]any{"v": map[string]any{"k": "s"}}, "author": "data-wins", "step": "data-step"}
	f := ScopeFacts(data, trusted, "review", "lens", nil)
	for k, want := range map[string]any{"repo": "acme/api", "owner": "acme", "name": "api", "number": 7, "pr": 7,
		"head": "abc", "base": "main", "url": "u", "kind": "review_requested", "title": "t", "author": "data-wins",
		"workflow": "review", "step": "lens"} {
		if !reflect.DeepEqual(f[k], want) {
			t.Errorf("%s = %#v, want %#v", k, f[k], want)
		}
	}
	if _, ok := f["secrets"]; ok {
		t.Error("named secrets must not be a scope fact")
	}
	if _, ok := f["vaults"]; ok {
		t.Error("vault values must not be scope facts")
	}
	if got, _ := RenderScopeKey("team:{{.inputs.team}}", f); got != "team:payments" {
		t.Errorf("inputs: %q", got)
	}
	// A target the event's sender chose keys nothing.
	forged := trusted
	forged.TargetTrusted = false
	f = ScopeFacts(data, forged, "review", "lens", nil)
	for _, k := range []string{"repo", "owner", "name", "head", "base", "url"} {
		if f[k] != "" {
			t.Errorf("forged target: %s = %q", k, f[k])
		}
	}
	for _, k := range []string{"number", "pr", "issue"} {
		if f[k] != 0 {
			t.Errorf("forged target: %s = %v", k, f[k])
		}
	}
	if _, err := RenderScopeKey("standard:{{.repo}}", f); err == nil {
		t.Error("a forged repo must not render into a key")
	}
	// The redactor sees the facts.
	f = ScopeFacts(map[string]any{"leak": "tok-123"}, trusted, "", "", func(v any) any {
		m := v.(map[string]any)
		m["leak"] = "[redacted]"
		return m
	})
	if f["leak"] != "[redacted]" {
		t.Errorf("redact not applied: %v", f["leak"])
	}
}

func TestRenderGrantScopes(t *testing.T) {
	facts := map[string]any{"repo": "acme/api", "head_ref": "*", "inputs": map[string]any{"team": "payments"}}
	got := RenderGrantScopes(map[string]map[string][]string{
		"memory.remember": {"scope": {"team/*", "team:{{.inputs.team}}", "branch:{{.head_ref}}", "x:{{.inputs.none}}", "bad\x01"}},
		"memory.recall":   {"scope": {"x:{{.inputs.none}}"}},
		"memory.forget":   {},
		"gh.comment":      {"repo": {"acme/*"}},
	}, facts)
	want := map[string][]ScopePattern{
		"memory.remember": {{Text: "team/*"}, {Text: "team:payments", Literal: true}, {Text: "branch:*", Literal: true}},
		"memory.recall":   {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	if got["memory.recall"] == nil {
		t.Fatal("a list that renders to nothing is a restriction (non-nil), not no restriction")
	}
	if RenderGrantScopes(nil, facts) != nil {
		t.Fatal("no grant, nothing rendered")
	}
	if !strings.Contains(ErrScopeFact.Error(), "no value") {
		t.Fatal(ErrScopeFact)
	}
}

// Facts a caller hands over (JSON: every number a float64) are judged as a
// run's: whole numbers become ints, secrets and vaults are not facts, the
// redactor sees the rest, and the caller's map is left alone.
func TestCallerFacts(t *testing.T) {
	in := map[string]any{"n": float64(42), "z": float64(0), "half": 1.5,
		"inputs": map[string]any{"pr": float64(7)}, "l": []any{float64(3)},
		"secrets": map[string]any{"gh": "t"}, "vaults": map[string]any{}, "s": "tok"}
	f := CallerFacts(in, func(v any) any { v.(map[string]any)["s"] = "[r]"; return v })
	want := map[string]any{"n": 42, "z": 0, "half": 1.5, "inputs": map[string]any{"pr": 7}, "l": []any{3}, "s": "[r]"}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("got %#v\nwant %#v", f, want)
	}
	if in["s"] != "tok" || in["n"] != float64(42) {
		t.Fatalf("the caller's facts were modified: %#v", in)
	}
	if f := CallerFacts(nil, nil); f == nil || len(f) != 0 {
		t.Fatalf("nil facts: %#v", f)
	}
	for _, c := range []struct {
		key   string
		facts map[string]any
	}{
		{"x:{{.v}}", map[string]any{"v": float64(0)}},
		{"x:{{.v}}", map[string]any{"v": int64(0)}},
		{"x:{{.v}}", map[string]any{"v": uint(0)}},
		{"x:{{index .l 0}}", map[string]any{"l": []any{""}}},
		{"x:{{index .l 0}}", map[string]any{"l": []any{nil}}},
		{"x:{{index .l 0}}", map[string]any{"l": []string{""}}},
	} {
		if got, err := RenderScopeKey(c.key, c.facts); err == nil {
			t.Errorf("%q over %#v must not render, got %q", c.key, c.facts, got)
		}
	}
	if got, _ := RenderScopeKey("x:{{.v | default \"d\"}}", map[string]any{"v": float64(0)}); got != "x:d" {
		t.Errorf("default sees a zero number as no value: %q", got)
	}
	if got, _ := RenderScopeKey("x:{{index .l 1}}", map[string]any{"l": []any{"", "b"}}); got != "x:b" {
		t.Errorf("a set list element renders: %q", got)
	}
}

// A fact the step lacks — absent, or present as null — never renders through
// print/printf as fmt's `<nil>` or `%!s(<nil>)`: the key does not apply, in
// every function an absent fact can reach. Handed to default/coalesce it
// still falls back. fmt's error marks are refused whatever produced them.
func TestAbsentFactsNeverRender(t *testing.T) {
	empty := map[string]any{"inputs": map[string]any{"gone": nil}, "nothing": nil, "l": []any{nil}}
	for _, key := range []string{
		`{{print .missing}}`, `{{printf "%v" .missing}}`, `{{printf "%s" .missing}}`,
		`n:{{printf "%d" .missing}}`, `team:{{printf "%s" .inputs.missing}}`,
		`team:{{.inputs.missing | printf "%s"}}`, `{{print "a" .nothing}}`,
		`x:{{printf "%s" .inputs.gone}}`, `x:{{print (index .l 0)}}`, `x:{{print .l}}`,
		`x:{{printf "%s" (index .inputs "nope")}}`, `x:{{printf "%s"}}`, `x:{{printf "%d" "s"}}`,
		`x:{{printf "%s" (printf "%s" .missing)}}`, `x:{{print (coalesce .missing .nothing)}}`,
	} {
		if got, err := RenderScopeKey(key, empty); err == nil {
			t.Errorf("%s must not render, got %q", key, got)
		}
		if _, err := RenderScopeKey("k:{{print .missing}}", empty); err == nil || !strings.Contains(err.Error(), ErrScopeFact.Error()) {
			t.Errorf("an absent fact must read as no value, not as a malformed key: %v", err)
		}
		if got := RenderScopeKeys([]string{key}, empty); len(got) != 0 {
			t.Errorf("%s must drop from a selector, got %q", key, got)
		}
	}
	for key, want := range map[string]string{
		`team:{{coalesce (printf "%s" .missing) "fallback"}}`:   "team:fallback",
		`team:{{printf "%s" .inputs.missing | default "core"}}`: "team:core",
		`x:{{.inputs.gone | default "d"}}`:                      "x:d",
		`x:{{coalesce .nothing (print .missing) "c"}}`:          "x:c",
	} {
		if got, err := RenderScopeKey(key, empty); err != nil || got != want {
			t.Errorf("%s = %q %v, want %q", key, got, err, want)
		}
	}
	// A grant entry reading an absent fact admits nothing.
	g := RenderGrantScopes(map[string]map[string][]string{"memory.remember": {"scope": {`team:{{printf "%s" .inputs.team}}`}}}, empty)
	if l, ok := g["memory.remember"]; !ok || len(l) != 0 {
		t.Fatalf("grant over an absent fact: %#v", g)
	}
	// A fact's own fmt-like text is refused too: fail closed.
	for _, title := range []string{"100%!", "x <nil>"} {
		if _, err := RenderScopeKey("t:{{.title}}", map[string]any{"title": title}); err == nil {
			t.Errorf("a rendered %q must refuse the key", title)
		}
	}
	// A fact takes no arguments, so a method could never be called.
	for _, key := range []string{`{{.title.Foo "x"}}`, `{{"x" | .title}}`, `{{.a.b 1}}`} {
		if err := CheckScopeKey(key); err == nil {
			t.Errorf("%s must be refused", key)
		}
	}
}

// default/coalesce whose every candidate has no value give no value — never
// "", so never the partial key `x:`.
func TestFallbackWithNoValueIsNoValue(t *testing.T) {
	f := map[string]any{"e": "", "z": 0, "n": nil}
	for _, key := range []string{`x:{{coalesce .missing .n .e .z}}`, `x:{{.e | default ""}}`, `x:{{.missing | default .z}}`, `x:{{coalesce}}`} {
		if got, err := RenderScopeKey(key, f); err == nil {
			t.Errorf("%s must not render, got %q", key, got)
		}
	}
	if got, _ := RenderScopeKey(`x:{{coalesce .missing .e "c"}}`, f); got != "x:c" {
		t.Errorf("coalesce reaches its first value: %q", got)
	}
}
