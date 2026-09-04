// Package flow executes the connectors-model trigger grammar: a fired
// trigger's steps (agents, commands, code, verbs, workflow calls) and hooks
// (workflow- and step-level, position-scoped), with event grouping, control
// flow, and per-step resume checkpoints.
//
// The engine routes a trigger here when its lowered action carries a FlowRef;
// legacy actions keep the legacy path. flow never imports the engine — the
// engine injects the agent-dispatch services it already owns.
package flow

import (
	"context"
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/kv"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// templateFuncs is the pinned function set templates may call, mirroring the
// expression language: default and coalesce. `{{.x | default "fb"}}` (or
// `{{default "fb" .x}}`) yields the fallback when .x is absent or empty;
// `{{coalesce .a .b "z"}}` yields the first present, non-empty argument.
// nil and "" count as empty; 0 and false are real values.
var templateFuncs = template.FuncMap{
	"default": func(fallback any, v ...any) any {
		if len(v) == 0 || templateEmpty(v[0]) {
			return fallback
		}
		return v[0]
	},
	"coalesce": func(vals ...any) any {
		for _, v := range vals {
			if !templateEmpty(v) {
				return v
			}
		}
		return ""
	},
	// kv reads a defined store inline: {{ kv "store" "namespace" "key" }}.
	// A missing/expired key yields nil, so it composes with default:
	// {{ kv "cache" "runs" .key | default 0 }}. Read-only.
	"kv": func(args ...any) (any, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("kv takes (store, namespace, key), got %d args", len(args))
		}
		st, err := kv.Use(fmt.Sprint(args[0]))
		if err != nil {
			return nil, err
		}
		v, found, err := st.Get(fmt.Sprint(args[1]), fmt.Sprint(args[2]))
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, nil
		}
		return v, nil
	},
	// vault reads a defined vaults: entry inline:
	// {{ vault "op" "Private/GitHub/token" }}. The value is tainted
	// sensitive (redacted from logs/audit) by the vaults read path.
	// Read-only — writes go through the <vault>.write verb.
	"vault": func(args ...any) (string, error) {
		if len(args) != 2 {
			return "", fmt.Errorf("vault takes (vault, key), got %d args", len(args))
		}
		return vaults.Read(context.Background(), fmt.Sprint(args[0]), fmt.Sprint(args[1]))
	},
	// kvContains tests membership in a stored list, read-only:
	// {{ kvContains "store" "namespace" "key" .item }}. An absent key is
	// false. The template surface stays side-effect-free — kv and kvContains
	// only read; mutations go through the kv.* verbs or ctx.store in code.
	"kvContains": func(args ...any) (bool, error) {
		if len(args) != 4 {
			return false, fmt.Errorf("kvContains takes (store, namespace, key, item), got %d args", len(args))
		}
		st, err := kv.Use(fmt.Sprint(args[0]))
		if err != nil {
			return false, err
		}
		return st.Contains(fmt.Sprint(args[1]), fmt.Sprint(args[2]), args[3])
	},
}

// templateEmpty mirrors expr's emptiness rule: nil or an empty string.
func templateEmpty(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

// render evaluates one template string against data. Strings without template
// actions pass through untouched. missingkey=zero matches the dispatch
// package's behavior: an absent key renders as its zero value.
func render(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("t").Option("missingkey=zero").Funcs(templateFuncs).Parse(s)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", snippet(s), err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template %q: %w", snippet(s), err)
	}
	out := b.String()
	// text/template renders a missing map key's zero value as "<no value>";
	// normalize to empty so conditions and posts don't leak the marker.
	return strings.ReplaceAll(out, "<no value>", ""), nil
}

// renderValue renders template strings anywhere inside a YAML-shaped value —
// map values, list elements, nested — leaving non-strings untouched. A value
// that is EXACTLY one template action resolving to a non-string (a list, a
// map, a number) is replaced by the underlying value rather than its string
// form, so `items: "{{.group.events}}"` stays a list.
func renderValue(v any, data map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		if path, ok := soleFieldRef(x); ok {
			if resolved, found := lookupPath(data, path); found {
				return resolved, nil
			}
			return nil, nil
		}
		return render(x, data)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := renderValue(e, data)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := renderValue(e, data)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []string:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := renderValue(e, data)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

// RenderOptions renders every templated value in an options map — exported
// for the notify.via router (main wiring), which invokes connector verbs
// outside a workflow scope.
func RenderOptions(opts map[string]any, data map[string]any) (map[string]any, error) {
	return renderOptions(opts, data)
}

// renderOptions renders every templated value in an options map.
func renderOptions(opts map[string]any, data map[string]any) (map[string]any, error) {
	if len(opts) == 0 {
		return map[string]any{}, nil
	}
	v, err := renderValue(opts, data)
	if err != nil {
		return nil, err
	}
	return v.(map[string]any), nil
}

// soleFieldRef reports whether s is exactly one "{{.a.b}}" action (with
// optional surrounding space) and returns its dotted path.
func soleFieldRef(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{{") || !strings.HasSuffix(t, "}}") || strings.Count(t, "{{") != 1 {
		return "", false
	}
	inner := strings.TrimSpace(t[2 : len(t)-2])
	if !strings.HasPrefix(inner, ".") {
		return "", false
	}
	path := strings.TrimPrefix(inner, ".")
	if path == "" || strings.ContainsAny(path, " \t|(){}\"'") {
		return "", false
	}
	return path, true
}

// lookupPath walks a dotted path through nested maps (and []any indices are
// not supported — paths address named fields only).
func lookupPath(data map[string]any, path string) (any, bool) {
	cur := any(data)
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// snippet trims a template string for error messages.
func snippet(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// templateRefs extracts the root-level dotted field paths a template string
// reads ({{.a.b}} → "a.b"), for load-time reference validation. Fields inside
// range/with bodies whose dot is rebound are skipped (they are relative to
// the range element, not the root scope); the range's own pipeline IS
// collected. Parse errors return the error so validate can surface them.
func templateRefs(s string) ([]string, error) {
	if !strings.Contains(s, "{{") {
		return nil, nil
	}
	t, err := template.New("t").Funcs(templateFuncs).Parse(s)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, tmpl := range t.Templates() {
		if tmpl.Tree != nil && tmpl.Tree.Root != nil {
			collectRefs(tmpl.Tree.Root, true, &refs)
		}
	}
	return refs, nil
}

// collectRefs walks a template parse tree. rootDot reports whether dot is
// still the root data map in this node's scope.
func collectRefs(n parse.Node, rootDot bool, out *[]string) {
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, c := range x.Nodes {
			collectRefs(c, rootDot, out)
		}
	case *parse.ActionNode:
		collectPipe(x.Pipe, rootDot, out)
	case *parse.IfNode:
		collectPipe(x.Pipe, rootDot, out)
		collectRefs(x.List, rootDot, out)
		if x.ElseList != nil {
			collectRefs(x.ElseList, rootDot, out)
		}
	case *parse.RangeNode:
		collectPipe(x.Pipe, rootDot, out)
		// Inside the body dot is each element — refs there are not root reads.
		collectRefs(x.List, false, out)
		if x.ElseList != nil {
			collectRefs(x.ElseList, rootDot, out)
		}
	case *parse.WithNode:
		collectPipe(x.Pipe, rootDot, out)
		collectRefs(x.List, false, out)
		if x.ElseList != nil {
			collectRefs(x.ElseList, rootDot, out)
		}
	}
}

func collectPipe(p *parse.PipeNode, rootDot bool, out *[]string) {
	if p == nil || !rootDot {
		return
	}
	for _, cmd := range p.Cmds {
		for _, arg := range cmd.Args {
			if f, ok := arg.(*parse.FieldNode); ok {
				*out = append(*out, strings.Join(f.Ident, "."))
			}
		}
	}
}

// templateVaultCalls extracts the (vault, key) pairs of every `vault`
// function call with literal arguments in a template string, for load-time
// validation of the vault names. Non-literal names can't be checked at load
// (they are rejected elsewhere: config-field refs must be literal).
func templateVaultCalls(s string) ([][2]string, error) {
	if !strings.Contains(s, "{{") {
		return nil, nil
	}
	t, err := template.New("t").Funcs(templateFuncs).Parse(s)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, tmpl := range t.Templates() {
		if tmpl.Tree != nil && tmpl.Tree.Root != nil {
			collectVaultCalls(tmpl.Tree.Root, &out)
		}
	}
	return out, nil
}

func collectVaultCalls(n parse.Node, out *[][2]string) {
	switch x := n.(type) {
	case *parse.ListNode:
		if x == nil {
			return
		}
		for _, c := range x.Nodes {
			collectVaultCalls(c, out)
		}
	case *parse.ActionNode:
		collectVaultPipe(x.Pipe, out)
	case *parse.IfNode:
		collectVaultPipe(x.Pipe, out)
		collectVaultCalls(x.List, out)
		if x.ElseList != nil {
			collectVaultCalls(x.ElseList, out)
		}
	case *parse.RangeNode:
		collectVaultPipe(x.Pipe, out)
		collectVaultCalls(x.List, out)
		if x.ElseList != nil {
			collectVaultCalls(x.ElseList, out)
		}
	case *parse.WithNode:
		collectVaultPipe(x.Pipe, out)
		collectVaultCalls(x.List, out)
		if x.ElseList != nil {
			collectVaultCalls(x.ElseList, out)
		}
	}
}

func collectVaultPipe(p *parse.PipeNode, out *[][2]string) {
	if p == nil {
		return
	}
	for _, cmd := range p.Cmds {
		if len(cmd.Args) == 0 {
			continue
		}
		id, ok := cmd.Args[0].(*parse.IdentifierNode)
		if !ok || id.Ident != "vault" {
			continue
		}
		pair := [2]string{}
		for i := 1; i < len(cmd.Args) && i <= 2; i++ {
			if s, isStr := cmd.Args[i].(*parse.StringNode); isStr {
				pair[i-1] = s.Text
			}
		}
		*out = append(*out, pair)
	}
}

// baseData builds the root template scope for a fired trigger: the target's
// addressable facts, the event's published context, and the named secrets.
func baseData(t core.Trigger, secrets map[string]string) map[string]any {
	d := map[string]any{
		"repo": t.Target.Repo, "owner": t.Target.Owner, "name": t.Target.Name,
		"pr": t.Target.PR, "issue": t.Target.Issue, "number": t.Target.Number,
		"head": t.Target.HeadSHA, "base": t.Target.BaseRef, "url": t.Target.HTMLURL,
		"kind": t.Kind, "title": t.Title,
	}
	for k, v := range t.Context {
		if _, ok := d[k]; !ok {
			d[k] = v
		}
	}
	if len(secrets) > 0 {
		sm := make(map[string]any, len(secrets))
		for k, v := range secrets {
			sm[k] = v
		}
		d["secrets"] = sm
	}
	return d
}

// addVaultData attaches the preloaded {{.vaults.<name>.<key>}} scope.
func addVaultData(d map[string]any, vaultVals map[string]map[string]string) {
	if len(vaultVals) == 0 {
		return
	}
	vm := make(map[string]any, len(vaultVals))
	for name, entries := range vaultVals {
		em := make(map[string]any, len(entries))
		for k, v := range entries {
			em[k] = v
		}
		vm[name] = em
	}
	d["vaults"] = vm
}
