package configedit

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	case p == "dashboard.auth.config_stepup" && fmt.Sprintf("%v", after) == "guarded":
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

// inertPaths are the only config paths conductor never acts on beyond display
// and estimates. With dashboard.auth.config_stepup: guarded, an edit confined
// to them applies on a signed-in session alone. Every other path is guarded,
// so a key added to the config later needs a step-up until it is listed here.
var inertPaths = [][]string{
	{"pricing"},
	{"usage", "mode"},
}

// guardedSections word the guarded paths for the review, most specific first:
// a changed leaf is named by the first entry whose path is a prefix of it, or
// by its top-level key. The dashboard, store and operators entries change who
// can sign in and where the sign-in state lives; the rest run something on
// this machine or change what does. imports is here because an imported file
// can define any of them.
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
	{[]string{"dashboard"}, "changes the dashboard block (whether and where the dashboard serves)"},
	{[]string{"store"}, "changes where conductor keeps its state and audit log, and with it the dashboard's sign-in store (store)"},
	{[]string{"imports"}, "changes which files the config imports (they can define operators, the dashboard and anything that runs)"},
	{[]string{"triggers"}, "changes triggers: their steps run commands, agents and plugins on this machine"},
	{[]string{"workflows"}, "changes workflows: their steps run commands, agents and plugins on this machine"},
	{[]string{"checks"}, "changes checks: they run steps on this machine"},
	{[]string{"connectors"}, "changes connectors: they run plugins and schedules on this machine"},
	{[]string{"runtimes"}, "changes runtimes: the programs agents run as"},
	{[]string{"hosts"}, "changes hosts: where conductor runs work"},
	{[]string{"engines"}, "changes engines: the programs conductor runs models with"},
	{[]string{"models"}, "changes models: what agents run on"},
	{[]string{"packs"}, "changes packs: they add triggers, workflows and plugins"},
	{[]string{"pack_trust"}, "changes which packs conductor trusts"},
	{[]string{"plugin_trust"}, "changes which plugins conductor trusts"},
	{[]string{"secrets"}, "changes which secrets templates can read (secrets)"},
	{[]string{"vaults"}, "changes where secrets are read from (vaults)"},
	{[]string{"stores"}, "changes the stores plugins and steps keep data in"},
	{[]string{"sandbox"}, "changes the agent sandbox"},
	{[]string{"browser"}, "changes conductor's browser for agents"},
	{[]string{"callable"}, "changes what outside callers can run (callable)"},
	{[]string{"policy"}, "changes the policy that limits what bots may do"},
	{[]string{"bots"}, "changes the bots: their tokens and what they act on"},
	{[]string{"teams"}, "changes teams: who bots take requests from"},
	{[]string{"update"}, "changes self-update: whether conductor installs and runs new releases"},
	{[]string{"dry_run"}, "changes dry_run: whether steps act for real"},
}

// inert reports whether path sits under an inert path.
func inert(path []string) bool {
	for _, p := range inertPaths {
		if hasPrefix(path, p) {
			return true
		}
	}
	return false
}

func hasPrefix(path, prefix []string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

// guardedEffect names the guarded section a changed path falls in.
func guardedEffect(path []string) (rank int, e Effect) {
	if len(path) == 0 {
		return len(guardedSections), Effect{Path: "(config)", Text: "changes the config as a whole"}
	}
	for i, g := range guardedSections {
		if hasPrefix(path, g.path) {
			return i, Effect{Path: pathKey(g.path), Text: g.text}
		}
	}
	return len(guardedSections), Effect{Path: path[0], Text: fmt.Sprintf("changes %s, which conductor acts on", path[0])}
}

// classify compares a config's text before and after an edit, however the edit
// was made (a staged leaf, a whole parent map or list, a restore): widening
// lists every leaf whose new value loosens a guardrail, and guarded every
// guarded section with a changed leaf (anything outside inertPaths).
func classify(before, after []byte) (widening, guarded []Effect) {
	b, _ := decodeText(before)
	a, err := decodeText(after)
	if err != nil {
		return nil, []Effect{{Path: "(config)", Text: "the new config doesn't parse"}}
	}
	if _, isMap := a.(map[string]any); !isMap && a != nil {
		return nil, []Effect{{Path: "(config)", Text: "replaces the whole config with something that isn't a map"}}
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
	type ranked struct {
		rank int
		e    Effect
	}
	seen := map[string]ranked{}
	for _, k := range keys {
		lb, inB := bl[k]
		la, inA := al[k]
		path := la.path
		if !inA {
			path = lb.path
		}
		if inA && inB && reflect.DeepEqual(lb.val, la.val) {
			continue
		}
		if !inert(path) {
			r, e := guardedEffect(path)
			seen[e.Path] = ranked{r, e}
		}
		switch {
		case !inA:
			if wideningDelete(lb.path) {
				widening = append(widening, Effect{Path: k, Text: "removes " + k, Widens: true})
			}
		case !inB:
			if isWidening(la.path, nil, la.val) {
				widening = append(widening, Effect{Path: k, Text: fmt.Sprintf("sets %s to %v", k, la.val), Widens: true})
			}
		default:
			if isWidening(la.path, lb.val, la.val) {
				widening = append(widening, Effect{Path: k, Text: fmt.Sprintf("changes %s from %v to %v", k, lb.val, la.val), Widens: true})
			}
		}
	}
	rs := make([]ranked, 0, len(seen))
	for _, r := range seen {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].rank != rs[j].rank {
			return rs[i].rank < rs[j].rank
		}
		return rs[i].e.Path < rs[j].e.Path
	})
	for _, r := range rs {
		guarded = append(guarded, r.e)
	}
	return widening, guarded
}

// leaf is one scalar of a decoded config, with its path.
type leaf struct {
	path []string
	val  any
}

// flatten records every scalar under v by its dotted path (list items by
// index). An empty map or list is a leaf of its own, except the document
// itself.
func flatten(v any, path []string, out map[string]leaf) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 && len(path) > 0 {
			out[pathKey(path)] = leaf{path: path, val: x}
		}
		for k, c := range x {
			flatten(c, append(append([]string(nil), path...), k), out)
		}
	case []any:
		if len(x) == 0 && len(path) > 0 {
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
