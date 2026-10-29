package configedit

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrStale is returned by Apply when the confirm token does not bind to the
// candidate being applied — the staged set changed since Review, so the
// operator must review again.
var ErrStale = errors.New("configedit: the review is stale (the staged changes changed since it was shown); review again")

// describe renders one change in words and reports whether it loosens a
// guardrail (a "widening" the review surfaces prominently). cur is the current
// value at the path, for a before→after phrasing.
func describe(c Change, cur any) (text string, widens bool) {
	path := pathKey(c.Path)
	if c.Delete {
		return fmt.Sprintf("removes %s", path), wideningDelete(c.Path)
	}
	after := fmt.Sprintf("%v", c.Value)
	if cur == nil {
		text = fmt.Sprintf("sets %s to %s", path, after)
	} else {
		text = fmt.Sprintf("changes %s from %v to %s", path, cur, after)
	}
	return text, isWidening(c.Path, cur, c.Value)
}

// isWidening encodes conductor's least-privilege guardrails: the handful of
// edits that loosen the security posture, named so the review can lead with
// them (strong-defaults-optin-footguns). The list mirrors the prototype's
// widening classifier (store.js), scoped to the scalar edits the dashboard
// stages.
func isWidening(path []string, before, after any) bool {
	p := pathKey(path)
	last := ""
	if len(path) > 0 {
		last = path[len(path)-1]
	}
	truthy := func(v any) bool {
		switch x := v.(type) {
		case bool:
			return x
		case string:
			return x == "true" || x == "yes" || x == "on"
		}
		return false
	}
	switch {
	// isolation mode set to none, anywhere in the tree (steps, packs, runtimes).
	case last == "mode" && contains(path, "isolation") && fmt.Sprintf("%v", after) == "none":
		return true
	// the agent sandbox turned off.
	case p == "sandbox.agents" && fmt.Sprintf("%v", after) == "off":
		return true
	// egress widened to a wildcard.
	case contains(path, "egress") && (fmt.Sprintf("%v", after) == "*" || strings.HasPrefix(fmt.Sprintf("%v", after), "*.")):
		return true
	// a trigger made callable.
	case last == "callable" && truthy(after) && !truthy(before):
		return true
	// a browser identity's evaluate turned on.
	case last == "evaluate" && contains(path, "browser") && fmt.Sprintf("%v", after) != "off" && fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after):
		return true
	// unattended autonomy switched on.
	case last == "unattended" && truthy(after) && !truthy(before):
		return true
		// the dashboard exposed beyond localhost would be a widening, but listen is
		// a list, not a scalar — the dashboard stages that as a section add.
	// dashboard sign-in loosened: a password allowed without TOTP, plain HTTP
	// acknowledged on a dashboard listener, or the tailnet listener's TLS off.
	case p == "dashboard.auth.password_requires_totp" && !truthy(after) && fmt.Sprintf("%v", after) != "":
		return true
	case contains(path, "dashboard") && last == "allow_plain_http" && truthy(after) && !truthy(before):
		return true
	case p == "dashboard.tailscale.tls" && (fmt.Sprintf("%v", after) == "off" || fmt.Sprintf("%v", after) == "false"):
		return true
	}
	return false
}

// wideningDelete flags deletes that remove a guardrail (e.g. deleting an
// isolation block that pinned a sandbox).
func wideningDelete(path []string) bool {
	return contains(path, "isolation") || contains(path, "sandbox")
}

func contains(path []string, seg string) bool {
	for _, s := range path {
		if s == seg {
			return true
		}
	}
	return false
}

// guardedSections decide who can sign in to the dashboard and how. Any change
// to them is a change to the dashboard's credentials or exposure, so applying it
// needs a step-up whether or not it loosens anything. imports is here because an
// imported file can define operators or the dashboard block.
var guardedSections = []struct {
	path []string
	text string
}{
	{[]string{"operators"}, "changes the operators: who can sign in to the dashboard, and with which SSH key"},
	{[]string{"dashboard", "auth"}, "changes how the dashboard signs people in (dashboard.auth)"},
	{[]string{"dashboard", "bind"}, "changes the dashboard's extra listeners (dashboard.bind)"},
	{[]string{"dashboard", "listen"}, "changes where the dashboard listens (dashboard.listen)"},
	{[]string{"dashboard", "tailscale"}, "changes the dashboard's tailnet listener (dashboard.tailscale)"},
	{[]string{"dashboard", "session"}, "changes how long dashboard sessions last (dashboard.session)"},
	{[]string{"imports"}, "changes which files the config imports (they can define operators and the dashboard)"},
}

// classify compares a config's text before and after an edit, however the edit
// was made (a staged leaf, a whole parent map or list, a restore): widening
// lists every leaf whose new value loosens a guardrail, and guarded every
// guarded section that changed.
func classify(before, after []byte) (widening, guarded []Effect) {
	var b, a any
	_ = yaml.Unmarshal(before, &b)
	if err := yaml.Unmarshal(after, &a); err != nil {
		return nil, []Effect{{Path: "(config)", Text: "the new config doesn't parse"}}
	}
	for _, g := range guardedSections {
		if !reflect.DeepEqual(lookup(b, g.path), lookup(a, g.path)) {
			guarded = append(guarded, Effect{Path: pathKey(g.path), Text: g.text, Widens: false})
		}
	}
	bl, al := map[string]leaf{}, map[string]leaf{}
	flatten(b, nil, bl)
	flatten(a, nil, al)
	keys := make([]string, 0, len(bl)+len(al))
	for k := range bl {
		keys = append(keys, k)
	}
	for k := range al {
		if _, ok := bl[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lb, inB := bl[k]
		la, inA := al[k]
		switch {
		case !inA:
			if wideningDelete(lb.path) {
				widening = append(widening, Effect{Path: k, Text: "removes " + k, Widens: true})
			}
		case !inB:
			if isWidening(la.path, nil, la.val) {
				widening = append(widening, Effect{Path: k, Text: fmt.Sprintf("sets %s to %v", k, la.val), Widens: true})
			}
		case !reflect.DeepEqual(lb.val, la.val):
			if isWidening(la.path, lb.val, la.val) {
				widening = append(widening, Effect{Path: k, Text: fmt.Sprintf("changes %s from %v to %v", k, lb.val, la.val), Widens: true})
			}
		}
	}
	return widening, guarded
}

// leaf is one scalar of a decoded config, with its path.
type leaf struct {
	path []string
	val  any
}

// flatten records every scalar under v by its dotted path (list items by
// index). An empty map or list is a leaf of its own.
func flatten(v any, path []string, out map[string]leaf) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			out[pathKey(path)] = leaf{path: path, val: x}
		}
		for k, c := range x {
			flatten(c, append(append([]string(nil), path...), k), out)
		}
	case []any:
		if len(x) == 0 {
			out[pathKey(path)] = leaf{path: path, val: x}
		}
		for i, c := range x {
			flatten(c, append(append([]string(nil), path...), strconv.Itoa(i)), out)
		}
	default:
		if len(path) > 0 {
			out[pathKey(path)] = leaf{path: path, val: x}
		}
	}
}

// lookup returns the decoded value at path, or nil.
func lookup(v any, path []string) any {
	for _, seg := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[seg]
	}
	return v
}
