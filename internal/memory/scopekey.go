package memory

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode"

	"github.com/NodeSpy/conductor/internal/core"
)

// SCOPE KEYS ARE TEMPLATES.
//
// A scope is an opaque key, and which keys a deployment uses is its own
// choice: one keys memory by repo, another by team, an owner/org, a label, an
// input the workflow was called with. So wherever an operator writes a key —
// a step's `memory:` scope/scopes/extra_scopes, a memory verb's `scope:`
// grant, the `scope:` a config-authored `memory.remember` writes — it is a
// template rendered over the step's facts (ScopeFacts):
//
//	team:{{.inputs.team}}     standard:{{.owner}}     label:{{index .labels 0}}
//
// `${repo}`, `${workflow}` and `${step}` remain as aliases for `{{.repo}}`,
// `{{.workflow}}` and `{{.step}}`.
//
// The rules, each fail-closed:
//
//   - A key that reads a fact this step has no value for — absent, null, ""
//     or a zero number — does not render. Recall drops it; a grant entry
//     admits nothing; a write is refused. `team:{{.inputs.team}}` with no
//     team is never `team:`, and `{{printf "%s" .inputs.team}}` is never
//     `%!s(<nil>)`. A fact with no value handed straight to default or
//     coalesce falls back: `{{.x | default "core"}}`,
//     `{{coalesce (printf "t-%s" .x) "core"}}`.
//   - Pipelines only: no if/range/with/define/template, no variables, and
//     only the functions default, coalesce, index, print and printf — no kv,
//     vault, secret or memory reads. Rendering a key does no work.
//   - A rendered value is LITERAL. In a grant it matches by equality, never
//     as a glob, so a fact that renders to `*` widens nothing (see
//     ScopePattern). Rendered output is never rendered again.
//   - A key carrying a control character, rendered or written, is refused.

// scopeAliases are the placeholder spellings the memory selector shipped
// with, kept as aliases of the template references.
var scopeAliases = strings.NewReplacer("${repo}", "{{.repo}}", "${workflow}", "{{.workflow}}", "${step}", "{{.step}}")

// scopeFuncNames is the closed function set a scope key may call.
var scopeFuncNames = map[string]bool{"default": true, "coalesce": true, "index": true, "print": true, "printf": true}

// emptyFactProbe stands in for a fact with no value on the second render
// (see RenderScopeKey). It carries a NUL, which no real key may contain.
const emptyFactProbe = "\x00empty-fact\x00"

// absentFact is what print and printf return when handed a fact that is
// ABSENT (or null): a value no real key may contain, which default and
// coalesce treat as no value and which refuses any key it reaches. The probe
// cannot see an absent fact — there is nothing in the facts to replace — so
// the functions that would otherwise format it as `<nil>` mark it instead.
const absentFact = "\x00absent-fact\x00"

// ErrScopeFact is the error a key that reads a fact this step has no value
// for renders with.
var ErrScopeFact = errors.New("reads a fact this step has no value for")

// IsScopeTemplate reports whether a key is rendered at all (it has `{{` or
// one of the `${…}` aliases). A key that is not is used verbatim.
func IsScopeTemplate(key string) bool {
	return strings.Contains(scopeAliases.Replace(key), "{{")
}

func scopeFuncs(empty func(any) bool) template.FuncMap {
	return template.FuncMap{
		"print": func(args ...any) string {
			if anyAbsent(args) {
				return absentFact
			}
			return fmt.Sprint(args...)
		},
		"printf": func(format string, args ...any) string {
			if anyAbsent(args) {
				return absentFact
			}
			return fmt.Sprintf(format, args...)
		},
		// default and coalesce fall back past a fact with no value — and when
		// what they fall back to has none either, the answer is an absent
		// fact, never "": `x:{{.a | default ""}}` is not the key `x:`.
		"default": func(fallback any, v ...any) any {
			if len(v) > 0 && !empty(v[0]) {
				return v[0]
			}
			if empty(fallback) {
				return absentFact
			}
			return fallback
		},
		"coalesce": func(vals ...any) any {
			for _, v := range vals {
				if !empty(v) {
					return v
				}
			}
			return absentFact
		},
	}
}

// anyAbsent reports whether print/printf were handed an absent fact: a nil
// (missingkey=zero's value for a key the facts lack, and a present key whose
// value is null), or the marker another print/printf already returned.
func anyAbsent(args []any) bool {
	for _, a := range args {
		if a == nil || a == absentFact {
			return true
		}
	}
	return false
}

// factEmpty is what default/coalesce treat as no value: the same empties the
// probe replaces (nil, "", a zero number), the probe itself, and an absent
// fact print/printf marked.
func factEmpty(v any) bool {
	if x, ok := v.(string); ok && x == absentFact {
		return true
	}
	// probeValue maps every empty to the probe and leaves the probe itself
	// as it is, so this one comparison covers both.
	return probeValue(v) == emptyFactProbe
}

// CheckScopeKey validates a key as written — what config load runs, so a
// typo fails there rather than silently dropping on every run.
func CheckScopeKey(key string) error {
	if err := refuseControl(key); err != nil {
		return err
	}
	_, err := parseScopeKey(key)
	return err
}

func refuseControl(s string) error {
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("scope key %q carries a control character", s)
		}
	}
	return nil
}

// parseScopeKey parses a key's template (nil for a static key) and refuses
// anything but pipelines over the closed function set.
func parseScopeKey(key string) (*template.Template, error) {
	src := scopeAliases.Replace(key)
	if !strings.Contains(src, "{{") {
		return nil, nil
	}
	t, err := template.New("scope").Option("missingkey=zero").Funcs(scopeFuncs(factEmpty)).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("scope key %q: %w", key, err)
	}
	for _, tt := range t.Templates() {
		if tt.Name() != "scope" {
			return nil, fmt.Errorf("scope key %q: define/template is not allowed in a scope key", key)
		}
	}
	if err := checkScopeNodes(t.Tree.Root); err != nil {
		return nil, fmt.Errorf("scope key %q: %w", key, err)
	}
	return t, nil
}

func checkScopeNodes(list *parse.ListNode) error {
	for _, n := range list.Nodes {
		switch x := n.(type) {
		case *parse.TextNode:
		case *parse.ActionNode:
			if err := checkScopePipe(x.Pipe); err != nil {
				return err
			}
		default:
			return fmt.Errorf("only {{…}} values are allowed in a scope key (no if/range/with/template)")
		}
	}
	return nil
}

func checkScopePipe(p *parse.PipeNode) error {
	if p == nil {
		return nil
	}
	if len(p.Decl) > 0 {
		return fmt.Errorf("variables are not allowed in a scope key")
	}
	for i, cmd := range p.Cmds {
		// A fact is a value, never a call: `.x "arg"` or `… | .x` would
		// invoke a method if a fact ever carried one. Facts are JSON-plain
		// today (maps, lists, strings, numbers, bools), so this only keeps
		// it that way for whatever fact source comes next.
		switch cmd.Args[0].(type) {
		case *parse.FieldNode, *parse.ChainNode, *parse.VariableNode:
			if len(cmd.Args) > 1 || i > 0 {
				return fmt.Errorf("a fact takes no arguments in a scope key (%s)", cmd.String())
			}
		}
		for _, a := range cmd.Args {
			switch x := a.(type) {
			case *parse.IdentifierNode:
				if !scopeFuncNames[x.Ident] {
					return fmt.Errorf("function %q is not allowed in a scope key (only default, coalesce, index, print, printf)", x.Ident)
				}
			case *parse.PipeNode:
				if err := checkScopePipe(x); err != nil {
					return err
				}
			case *parse.ChainNode:
				if pn, ok := x.Node.(*parse.PipeNode); ok {
					if err := checkScopePipe(pn); err != nil {
						return err
					}
				}
			case *parse.FieldNode, *parse.DotNode, *parse.StringNode, *parse.NumberNode, *parse.BoolNode, *parse.NilNode:
			case *parse.VariableNode:
				if x.Ident[0] != "$" {
					return fmt.Errorf("variables are not allowed in a scope key")
				}
			default:
				return fmt.Errorf("%q is not allowed in a scope key", a.String())
			}
		}
	}
	return nil
}

// RenderScopeKey renders one key over facts. A key with no template is
// returned verbatim. An error means the key does not apply to this step —
// the caller drops it (recall), admits nothing for it (grant) or refuses
// the write; it never falls back to a partial key.
//
// Empty facts are found by rendering twice: once for real, and once with
// every empty fact replaced by a probe the default/coalesce functions still
// treat as empty. A probe that reaches the output was read raw.
func RenderScopeKey(key string, facts map[string]any) (string, error) {
	if err := refuseControl(key); err != nil {
		return "", err
	}
	t, err := parseScopeKey(key)
	if err != nil {
		return "", err
	}
	if t == nil {
		return key, nil
	}
	var b strings.Builder
	if err := t.Execute(&b, facts); err != nil {
		return "", fmt.Errorf("scope key %q: %w", key, err)
	}
	out := b.String()
	if strings.Contains(out, "<no value>") || strings.Contains(out, absentFact) {
		return "", fmt.Errorf("scope key %q %w", key, ErrScopeFact)
	}
	// Backstop: whatever path a nil or a mismatched verb took to the output,
	// fmt's own error marks (`%!s(<nil>)`, `%!d(string=x)`, `%!(EXTRA …)`) and
	// a printed nil are never a key. A fact whose own text carries one is
	// refused too — fail closed.
	if strings.Contains(out, "%!") || strings.Contains(out, "<nil>") {
		return "", fmt.Errorf("scope key %q renders a formatting error or a nil (%q) — not a key", key, out)
	}
	b.Reset()
	if err := t.Execute(&b, probeFacts(facts)); err != nil || strings.Contains(b.String(), emptyFactProbe) {
		return "", fmt.Errorf("scope key %q %w", key, ErrScopeFact)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("scope key %q renders empty", key)
	}
	if err := refuseControl(out); err != nil {
		return "", err
	}
	return out, nil
}

// probeFacts copies facts with every empty value (nil, "", a zero number)
// replaced by the probe, recursing into nested maps (inputs, steps, …) and
// lists (labels, …).
func probeFacts(facts map[string]any) map[string]any {
	out := make(map[string]any, len(facts))
	for k, v := range facts {
		out[k] = probeValue(v)
	}
	return out
}

func probeValue(v any) any {
	switch x := v.(type) {
	case nil:
		return emptyFactProbe
	case string:
		if x == "" {
			return emptyFactProbe
		}
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		if reflect.ValueOf(x).IsZero() {
			return emptyFactProbe
		}
	case map[string]any:
		return probeFacts(x)
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = probeValue(e)
		}
		return l
	case []string:
		l := make([]string, len(x))
		for i, e := range x {
			if l[i] = e; e == "" {
				l[i] = emptyFactProbe
			}
		}
		return l
	}
	return v
}

// RenderScopeKeys renders a list of keys, dropping every key that does not
// render. A static key passes through verbatim; "" stays "" (the shared
// set, where a caller allows it).
func RenderScopeKeys(keys []string, facts map[string]any) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, err := RenderScopeKey(k, facts); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// ScopePattern is one grant entry ready to match. A static entry keeps its
// glob — the operator wrote `team/*` meaning that family. A rendered entry
// is Literal: matched by equality, so glob characters a fact carries never
// act as a pattern.
type ScopePattern struct {
	Text    string `json:"text"`
	Literal bool   `json:"literal,omitempty"`
}

// RenderGrantScopes renders every `scope:` list in a skill.verbs map form
// over one step's facts: verb pattern → its entries, ready to match. An
// entry that does not render is dropped (it admits nothing); a pattern whose
// list renders to nothing keeps an empty, non-nil list — a restriction that
// admits nothing, never no restriction. Computed where the step's facts
// exist (at dispatch) and carried with the session, so every face the
// step's agent writes through matches the same rendered list.
func RenderGrantScopes(verbScopes map[string]map[string][]string, facts map[string]any) map[string][]ScopePattern {
	var out map[string][]ScopePattern
	pats := make([]string, 0, len(verbScopes))
	for p := range verbScopes {
		pats = append(pats, p)
	}
	sort.Strings(pats)
	for _, pat := range pats {
		list, ok := verbScopes[pat][ScopeOption]
		if !ok {
			continue
		}
		rendered := make([]ScopePattern, 0, len(list))
		for _, e := range list {
			if !IsScopeTemplate(e) {
				if refuseControl(e) == nil {
					rendered = append(rendered, ScopePattern{Text: e})
				}
				continue
			}
			if v, err := RenderScopeKey(e, facts); err == nil {
				rendered = append(rendered, ScopePattern{Text: v, Literal: true})
			}
		}
		if out == nil {
			out = map[string][]ScopePattern{}
		}
		out[pat] = rendered
	}
	return out
}

// ScopeOption is the grant key a memory verb's scope list lives under.
const ScopeOption = "scope"

// ScopeFacts builds the facts a step's scope keys render over: the data the
// step's prompt sees (data — inputs, step outputs, the trigger's event
// context …), with
//
//   - the named secrets and vault values removed — a key is stored and shown
//     in prompts, so it must never carry one;
//   - the target facts (repo, owner, name, number, pr, issue, head, base,
//     url) set from the dispatch's OWN target, and blank when the event's
//     sender chose the target (core.Trigger.OwnRepo) — a forged repo must
//     not key another tenant's memory;
//   - workflow and step set to the run's memory context keys.
//
// redact, when set, scrubs tracked secret values a step output or the event
// context may have echoed.
func ScopeFacts(data map[string]any, t core.Trigger, workflow, step string, redact func(any) any) map[string]any {
	f := make(map[string]any, len(data)+len(t.Context)+16)
	for k, v := range t.Context {
		f[k] = v
	}
	for k, v := range data {
		f[k] = v
	}
	delete(f, "secrets")
	delete(f, "vaults")
	// Target facts only for the dispatch's OWN target (core.OwnRepo): when
	// the event's sender chose the target, every field of it is theirs, and
	// none may key memory. Blank, so a key reading one does not render.
	f["repo"], f["owner"], f["name"], f["head"], f["base"], f["url"] = "", "", "", "", "", ""
	f["number"], f["pr"], f["issue"] = 0, 0, 0
	if repo := t.OwnRepo(); repo != "" {
		f["repo"], f["owner"], f["name"] = repo, t.Target.Owner, t.Target.Name
		f["number"], f["pr"], f["issue"] = t.Target.Number, t.Target.PR, t.Target.Issue
		f["head"], f["base"], f["url"] = t.Target.HeadSHA, t.Target.BaseRef, t.Target.HTMLURL
	}
	f["kind"] = t.Kind
	if _, ok := f["title"]; !ok {
		f["title"] = t.Title
	}
	f["workflow"] = workflow
	f["step"] = step
	if redact != nil {
		if r, ok := redact(f).(map[string]any); ok {
			f = r
		}
	}
	return f
}

// CallerFacts readies facts a CALLER supplied — a code step's
// ctx.memory.key, JSON over the ctx socket — for RenderScopeKey, so they are
// judged as a run's facts are: whole JSON numbers become ints (a JSON 0 is a
// zero number, so it has no value; 42 renders "42"), named secrets and vault
// values are dropped as ScopeFacts drops them, and redact (the run's
// tracked-secret redactor) scrubs the rest. facts is not modified.
func CallerFacts(facts map[string]any, redact func(any) any) map[string]any {
	f := wholeNumbers(facts).(map[string]any) // a fresh map, even for nil facts
	delete(f, "secrets")
	delete(f, "vaults")
	if redact != nil {
		if r, ok := redact(f).(map[string]any); ok {
			f = r
		}
	}
	return f
}

// wholeNumbers deep-copies v with every whole float64 (how JSON decodes a
// number) as an int.
func wholeNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = wholeNumbers(e)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = wholeNumbers(e)
		}
		return l
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int(x)
		}
	}
	return v
}
