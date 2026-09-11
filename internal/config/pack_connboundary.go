package config

import (
	"fmt"
	"sort"
	"strings"
)

// THE PACK CONNECTOR BOUNDARY — `requires.connectors` for every reference,
// not just `skill.verbs`.
//
// A pack's `requires.connectors` is its CAPABILITY MANIFEST: the consumer
// sees it before installing, and the pack may not reach past it. That was
// enforced for `skill.verbs` (pack_skill.go) and NOWHERE ELSE, so a plain
// step escaped it completely:
//
//	# a pack with NO requires.connectors at all
//	steps:
//	  - uses: gh.comment          # guessed the operator's instance name
//	    options: { repo: "...", body: "..." }
//
// `rebindVerb` rewrites a connector prefix only when the name is DECLARED AND
// BOUND; an undeclared one passed through untouched, and at runtime execVerb
// resolved it against the consumer's real connectors. The pack needed only to
// guess a conventional instance name — gh, slack, kv, pagerduty — which is
// what conventional names are. A review pack could page, post, and write
// wherever the operator's credentials reached.
//
// This is a PRE-EXISTING hole in the pack system (#53/#55), not a regression
// from the scope work: `skill.verbs` was bounded when packs shipped and the
// plain-`uses:` surface never was.
//
// Every pack-authored reference now goes through one check: step `uses:`,
// hook `uses:`, a trigger's `on:` source, a session's `end_on:`, and the
// `store:` selector (the same shape — an undeclared store selector also
// passed through to the consumer's real store).

// packNamespaceRule says what a pack may do with one connector-namespace
// name without declaring it.
type packNamespaceRule int

const (
	// packNeedsDeclaring: the default. It names something of the operator's,
	// so requires.connectors has to say so.
	packNeedsDeclaring packNamespaceRule = iota
	// packOpenNamespace: conductor's own orchestration, carrying no operator
	// capability of its own.
	packOpenNamespace
	// packDeniedNamespace: never reachable from a pack, declared or not.
	packDeniedNamespace
)

// packNamespaces is the exact safe set, with the reason for each — because
// "which built-ins are safe" is the judgement call in this boundary and it
// should be readable, not inferred.
//
//	workflow  a pack's own orchestration. workflow.run/save/list address
//	          workflows BY NAME, and a pack's workflow names are namespaced to
//	          its instance at instantiation, so it can only ever call its own.
//	manual    a source kind (`on: manual`), not a verb namespace — it names no
//	          resource and grants nothing.
//
// Everything else must be declared, INCLUDING the data built-ins. kv, sql,
// memory and blob are the operator's storage, their shared agent memory, and
// their filesystem: a pack that uses them is asking for something, and the
// consumer should read that in the manifest before installing. They bind to
// themselves (they are always present, and the reserved names cannot appear
// in `connectors:`), so declaring one costs the author a line and tells the
// operator the truth.
//
//	conductor  DENIED. update/pause/resume/restart/reload/run are daemon
//	           control — a pack that restarts or updates the daemon, or fires
//	           the operator's own manual triggers by name, is not asking for a
//	           capability, it is taking the box. There is no declaration that
//	           makes it reasonable.
var packNamespaces = map[string]packNamespaceRule{
	"workflow":  packOpenNamespace,
	"manual":    packOpenNamespace,
	"conductor": packDeniedNamespace,
}

// packSelfBinding are the built-in namespaces a pack declares but the
// consumer does not bind: they are always present under their own name, and
// `connectors:` refuses the reserved names. Declaring is the point — the
// manifest says what the pack touches.
var packSelfBinding = map[string]bool{
	"kv": true, "sql": true, "memory": true, "blob": true,
}

// IsPackSelfBindingConnector reports whether a required connector name is a
// built-in that binds to itself.
func IsPackSelfBindingConnector(name string) bool { return packSelfBinding[name] }

// checkPackConnectorRefs verifies every connector reference a pack authored
// against its declared interface. declared is requires.connectors' names.
// Returns one problem string per offending reference, sorted.
func checkPackConnectorRefs(man *PackManifest) []string {
	declared := map[string]bool{}
	for _, n := range man.Pack.Requires.ConnectorNames() {
		declared[n] = true
	}
	var problems []string
	check := func(where, kind, ref string) {
		name, ok := packRefNamespace(ref)
		if !ok {
			return
		}
		switch packNamespaces[name] {
		case packOpenNamespace:
			return
		case packDeniedNamespace:
			problems = append(problems, fmt.Sprintf(
				"%s: %s %q — `%s.*` is daemon control (update/pause/restart/reload/run) and is never reachable from a pack, declared or not",
				where, kind, ref, name))
			return
		}
		if declared[name] {
			return
		}
		hint := fmt.Sprintf("declare it: requires: { connectors: { %s: \"*\" } }", name)
		if packSelfBinding[name] {
			hint = fmt.Sprintf("declare it: requires: { connectors: { %s: \"*\" } } — it binds to itself, so the consumer binds nothing; declaring is what tells them the pack touches their %s", name, name)
		}
		problems = append(problems, fmt.Sprintf(
			"%s: %s %q names connector %q, which the pack does not declare in requires.connectors — a pack may only reach connectors its manifest names, or it could address whatever the consumer happens to have called %q; %s",
			where, kind, ref, name, name, hint))
	}
	// checkBare is the same boundary for a reference that is a BARE connector
	// name rather than `conn.verb` — handoff:, approve_via:, notify via:.
	checkBare := func(where, kind, name string) {
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "{{") {
			return
		}
		switch packNamespaces[name] {
		case packOpenNamespace:
			return
		case packDeniedNamespace:
			problems = append(problems, fmt.Sprintf(
				"%s: %s %q — `%s` is daemon control and is never reachable from a pack",
				where, kind, name, name))
			return
		}
		if declared[name] {
			return
		}
		problems = append(problems, fmt.Sprintf(
			"%s: %s %q names connector %q, which the pack does not declare in requires.connectors — a pack may only reach connectors its manifest names; declare it: requires: { connectors: { %s: \"*\" } }",
			where, kind, name, name, name))
	}
	man.WalkPackSteps(func(where string, s *Step) {
		check(where, "uses:", s.Uses)
		// `handoff: slack` is a BARE connector name — the ask-capable
		// connector a background review is presented on. It is a connector
		// reference with no dot, which is exactly why the first version of
		// this walk missed it: every other site is `conn.something` (round-12
		// #2). A pack naming the consumer's slack here reaches it as surely
		// as `uses: slack.post` would.
		checkBare(where, "handoff:", s.Handoff)
		for i := range s.Hooks {
			check(fmt.Sprintf("%s hook[%d]", where, i), "hook uses:", s.Hooks[i].Uses)
		}
		if s.Session != nil {
			for _, e := range s.Session.EndOn {
				check(where, "session end_on:", e)
			}
		}
	})
	// A pack may ship a `policy:` block, and approve_via names the
	// ask-capable connector an approval is presented on — bare, like handoff.
	if man.Policy != nil && man.Policy.AgentAuthored != nil {
		checkBare("policy.agent_authored", "approve_via:", man.Policy.AgentAuthored.ApproveVia)
	}
	// (A pack manifest has no `notify:` block — notify.via lives on the
	// consumer's Config, which is theirs — so there is no route to walk here.)
	for ti := range man.Triggers {
		t := &man.Triggers[ti]
		label := t.Name
		if label == "" {
			label = fmt.Sprintf("triggers[%d]", ti)
		}
		check(label, "on:", t.On)
		for _, os := range t.OnSources {
			check(label, "on:", os.Source)
		}
		for i := range t.Hooks {
			check(fmt.Sprintf("%s hook[%d]", label, i), "hook uses:", t.Hooks[i].Uses)
		}
	}
	sort.Strings(problems)
	return uniq(problems)
}

// packRefNamespace extracts the connector-namespace half of a `conn.verb` /
// `conn.event` reference. Templated and bare references name no connector.
func packRefNamespace(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "{{") {
		return "", false
	}
	name, _, ok := strings.Cut(ref, ".")
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// checkPackStoreRefs is the same boundary for the `store:` selector, which
// had the identical shape: rebindStore rewrote a DECLARED store and let an
// undeclared one through to whatever the consumer called by that name.
func checkPackStoreRefs(man *PackManifest) []string {
	declared := map[string]bool{}
	for _, n := range man.Pack.Requires.Stores {
		declared[n] = true
	}
	var problems []string
	check := func(where string, opts map[string]any) {
		sel, _ := opts["store"].(string)
		if sel == "" || strings.Contains(sel, "{{") || declared[sel] {
			return
		}
		problems = append(problems, fmt.Sprintf(
			"%s: store %q is not declared in requires.stores — a pack may only touch stores its manifest names; declare it: requires: { stores: [%s] }",
			where, sel, sel))
	}
	man.WalkPackSteps(func(where string, s *Step) {
		check(where, s.Options)
		for i := range s.Hooks {
			check(fmt.Sprintf("%s hook[%d]", where, i), s.Hooks[i].Options)
		}
	})
	sort.Strings(problems)
	return uniq(problems)
}
