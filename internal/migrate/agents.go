package migrate

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The `agents:` removal pass (docs/design/agents-removal.md §7).
//
// `agents.<name>` was doing five jobs. Each moves to a home that is not an
// agent:
//
//	provider+model   -> model: on the step (an exact pin; a migration must
//	                    never invent a fleet)
//	budget           -> the runtime the agent ran on
//	behavior fields  -> a NAMED STEP in the top-level `steps:` registry,
//	                    played from each referencing step by `step: <name>`
//	memory / session / outcome opt-ins -> the named step (they ride with it)
//
// A named step rather than a YAML anchor, deliberately. Anchors are the
// reuse mechanism now (anchors.go) and this pass does emit one — for a
// profile that `extends:` another, where both sides land in the same
// section of the same file. But a REFERENCE cannot be an anchor: `agents:`
// commonly sits in the main config while the triggers that named it sit in
// `conf.d/*.yaml`, and a YAML anchor does not cross `imports:`. Emitting
// `<<: *fixer` there would produce a config that no longer parses, the
// re-validate would refuse, and a box that has already auto-updated past
// `agents:` would be stuck. A name resolves after imports merge, so it
// works wherever the profile was written.
//
// TRACK-RECORD CONTINUITY is the delicate part and the reason this is one
// pass rather than a mechanical field move. Memory scoping, session
// affinity, and outcome tracking all used to key off the AGENT NAME; they
// now key off the STEP IDENTITY. The identity ladder's first rung is an
// explicit `name:`, so the template is emitted under the OLD AGENT NAME and
// every step extending it inherits that name as its identity. The keys
// therefore come out byte-identical to what the box already has on disk:
//
//	outcomeStats["fixer"]        -> still outcomeStats["fixer"]
//	engagements[].agent "fixer"  -> still matches (the JSON tag is unchanged)
//	memory scope "agent:fixer"   -> still recalled (memory.LegacyStepScope)
//
// A user's accumulated history survives the upgrade. That is not a nicety:
// silently resetting a track record would make the outcome-feedback loop
// quietly wrong for weeks.
//
// It is a RAW-NODE pass, like applyUsePass: it never decodes into
// config.Config, so it keeps working after `agents:` left the schema — which
// is exactly when it is needed. The lenient posture holds: a field with no
// home is dropped WITH A NOTE, never a hard refusal, because a deployed box
// auto-updates into this binary and a migration that refuses is a crash-loop.

// behaviorKeys are the agent-profile fields that move VERBATIM onto the step
// template — same key, same meaning, new home.
var behaviorKeys = []string{
	"thinking", "mode", "workspace", "wait_timeout", "archive_when_done",
	"labels", "guidance", "memory", "session", "skill", "isolation",
	"outcome_feedback", "host", "runtime",
}

// applyAgentsPass rewrites `agents:` into named `steps:` entries, moves each
// profile's budget onto its runtime, and repoints every `agent: <name>`
// reference at `step: <name>`.
func applyAgentsPass(masked []byte, notes *[]string) (out []byte, changed bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(masked, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, nil
	}
	agents := mapValue(root, "agents")
	if agents == nil || agents.Kind != yaml.MappingNode || len(agents.Content) == 0 {
		// Nothing to migrate. (An `agents:` key present but empty still gets
		// removed below so the strict decoder does not trip on it.)
		if removeMapKey(root, "agents") {
			b, mErr := marshalDoc(&doc)
			if mErr != nil {
				return nil, false, mErr
			}
			*notes = append(*notes, "agents: was empty — removed (behavior now lives on the step; see docs/design/agents-removal.md)")
			return b, true, nil
		}
		return nil, false, nil
	}

	templates := &yaml.Node{Kind: yaml.MappingNode}
	names := make([]string, 0, len(agents.Content)/2)
	profileRuntime := map[string]string{}
	// parent[child] = the profile it extended. A named step cannot play
	// another named step, so these become a YAML anchor + merge key within
	// the emitted section — same file, same map, so the anchor resolves.
	parent := map[string]string{}

	for i := 0; i+1 < len(agents.Content); i += 2 {
		name, body := agents.Content[i].Value, agents.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		names = append(names, name)
		tmpl := &yaml.Node{Kind: yaml.MappingNode}

		// IDENTITY. The old agent name becomes the template's `name:`, which
		// is the step-identity ladder's top rung — this is what carries the
		// memory / session / outcome history across the upgrade.
		setMapKey(tmpl, "name", scalar(name))
		setMapKey(tmpl, "type", scalar("agent"))

		// provider + model -> a single exact model: pin. `provider:` alone
		// (no model) named a backend, not a model, so it maps to a bare
		// launch: omit model: entirely and let the runtime choose.
		model := scalarAt(body, "model")
		provider := scalarAt(body, "provider")
		switch {
		case model != "":
			setMapKey(tmpl, "model", scalar(model))
			if provider != "" {
				*notes = append(*notes, fmt.Sprintf(
					"agents.%s: provider: %s dropped — a model id identifies its provider; the runtime that offers %q is resolved from the roster", name, provider, model))
			}
		case provider != "":
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s: provider: %s named a backend, not a model — steps.%s now BARE LAUNCHES (no --model, the runtime's own default). Pin one with model:, or declare a fleet under models:", name, provider, name))
		}

		// extends: between profiles becomes a YAML anchor merge between the
		// emitted entries (wired up after every entry exists, below).
		if ext := scalarAt(body, "extends"); ext != "" {
			parent[name] = ext
		}

		// Behavior fields move verbatim.
		for _, k := range behaviorKeys {
			if v := nodeAt(body, k); v != nil {
				setMapKey(tmpl, k, v)
			}
		}
		// `controller:` was the pre-connectors spelling of `runtime:`.
		if nodeAt(body, "runtime") == nil {
			if c := scalarAt(body, "controller"); c != "" {
				setMapKey(tmpl, "runtime", scalar(c))
				*notes = append(*notes, fmt.Sprintf("agents.%s: controller: %s -> steps.%s.runtime", name, c, name))
			}
		}
		if rt := scalarAt(body, "runtime"); rt != "" {
			profileRuntime[name] = rt
		} else if c := scalarAt(body, "controller"); c != "" {
			profileRuntime[name] = c
		}

		// budget moves onto the RUNTIME: a budget caps execution cost on a
		// backend (design §1).
		if b := nodeAt(body, "budget"); b != nil {
			target := profileRuntime[name]
			if moveBudgetToRuntime(root, target, b, name, notes) {
				// moved (or reported); nothing stays on the step
				_ = target
			}
		}
		// Lenient posture: anything the decomposition has no home for is
		// reported, never dropped in silence.
		for _, k := range unhandledAgentKeys(body) {
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s.%s dropped — no equivalent on a step (the profile's five jobs moved to fleets, the runtime, and the step itself)", name, k))
		}
		setMapKey(templates, name, tmpl)
		*notes = append(*notes, fmt.Sprintf("agents.%s -> steps.%s, played by `step: %s` (name: pins the identity, so its memory/session/outcome history carries over)", name, name, name))
	}
	sort.Strings(names)

	// Merge into any existing `steps:` block rather than replacing it.
	target := mapValue(root, "steps")
	if target != nil && target.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(templates.Content); i += 2 {
			key := templates.Content[i].Value
			if nodeAt(target, key) != nil {
				*notes = append(*notes, fmt.Sprintf(
					"steps.%s already exists — kept it and DROPPED the agents.%s profile of the same name (rename one if they were meant to differ)", key, key))
				continue
			}
			setMapKey(target, key, templates.Content[i+1])
		}
	} else if len(templates.Content) > 0 {
		target = templates
		setMapKey(root, "steps", templates)
	}
	removeMapKey(root, "agents")

	linkProfileAnchors(target, parent, notes)

	// Repoint every `agent: <name>` reference at `step: <name>`.
	defined := map[string]bool{}
	for _, n := range names {
		defined[n] = true
	}
	rewriteAgentRefs(root, defined, notes)

	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// linkProfileAnchors turns each `agents.<child>.extends: <parent>` into a
// YAML anchor on the parent entry and a `<<: *parent` merge key on the
// child. Both live in the emitted `steps:` map, so the anchor is in scope.
//
// YAML requires the anchor to appear before the alias, so entries are
// reordered parents-first. A parent nothing defines, or a cycle, drops the
// link with a note — the lenient posture: a broken alias would make the
// whole file unparseable, which is far worse than a lost inheritance.
func linkProfileAnchors(steps *yaml.Node, parent map[string]string, notes *[]string) {
	if steps == nil || steps.Kind != yaml.MappingNode || len(parent) == 0 {
		return
	}
	index := map[string]int{}
	present := map[string]bool{}
	for i := 0; i+1 < len(steps.Content); i += 2 {
		index[steps.Content[i].Value] = i
		present[steps.Content[i].Value] = true
	}
	// Keep only links whose parent exists and whose chain terminates.
	linked := map[string]string{}
	for child, p := range parent {
		switch {
		case !present[p] || !present[child]:
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s.extends: %s dropped — no profile or steps: entry named %q to inherit from", child, p, p))
		case cyclic(child, parent):
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s.extends: %s dropped — the inheritance chain loops back on itself", child, p))
		default:
			linked[child] = p
		}
	}
	if len(linked) == 0 {
		return
	}
	// Parents first, so every `<<: *name` follows its `&name`.
	order := make([]string, 0, len(steps.Content)/2)
	placed := map[string]bool{}
	var emit func(string)
	emit = func(name string) {
		if placed[name] {
			return
		}
		placed[name] = true
		if p, ok := linked[name]; ok {
			emit(p)
		}
		order = append(order, name)
	}
	for i := 0; i+1 < len(steps.Content); i += 2 {
		emit(steps.Content[i].Value)
	}
	reordered := make([]*yaml.Node, 0, len(steps.Content))
	for _, name := range order {
		i := index[name]
		reordered = append(reordered, steps.Content[i], steps.Content[i+1])
	}
	steps.Content = reordered

	for child, p := range linked {
		base := nodeAt(steps, p)
		body := nodeAt(steps, child)
		if base == nil || body == nil || body.Kind != yaml.MappingNode {
			continue
		}
		base.Anchor = p
		body.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"},
			{Kind: yaml.AliasNode, Value: p, Alias: base},
		}, body.Content...)
		*notes = append(*notes, fmt.Sprintf(
			"agents.%s.extends: %s -> steps.%s merges the anchor &%s (YAML anchors are the reuse mechanism now; they are file-local, so this works because both entries land in this file)", child, p, child, p))
	}
}

// cyclic reports whether following parent links from name loops.
func cyclic(name string, parent map[string]string) bool {
	seen := map[string]bool{name: true}
	for cur, ok := parent[name]; ok; cur, ok = parent[cur] {
		if seen[cur] {
			return true
		}
		seen[cur] = true
	}
	return false
}

// handledAgentKeys are the profile keys the decomposition has a home for.
var handledAgentKeys = func() map[string]bool {
	m := map[string]bool{
		"extends": true, "provider": true, "model": true,
		"controller": true, "budget": true,
	}
	for _, k := range behaviorKeys {
		m[k] = true
	}
	return m
}()

// unhandledAgentKeys lists a profile's keys that nothing above consumed.
func unhandledAgentKeys(body *yaml.Node) []string {
	var out []string
	for i := 0; i+1 < len(body.Content); i += 2 {
		if k := body.Content[i].Value; !handledAgentKeys[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// moveBudgetToRuntime attaches a profile's budget to the runtimes: entry it
// ran on. With no runtime named, it lands on the `default: true` runtime;
// with none of those either, it is reported rather than silently dropped.
func moveBudgetToRuntime(root *yaml.Node, runtimeName string, budget *yaml.Node, profile string, notes *[]string) bool {
	rts := mapValue(root, "runtimes")
	if rts == nil || rts.Kind != yaml.MappingNode || len(rts.Content) == 0 {
		*notes = append(*notes, fmt.Sprintf(
			"agents.%s.budget dropped — a budget now caps execution cost on a RUNTIME, and this config declares no runtimes: entry to put it on. Re-add it as runtimes.<name>.budget", profile))
		return false
	}
	target := runtimeName
	if target == "" || nodeAt(rts, target) == nil {
		target = defaultRuntimeKey(rts)
	}
	if target == "" {
		*notes = append(*notes, fmt.Sprintf(
			"agents.%s.budget dropped — no runtimes: entry is flagged default: true, so there is no unambiguous runtime to cap. Re-add it as runtimes.<name>.budget", profile))
		return false
	}
	body := nodeAt(rts, target)
	if body == nil || body.Kind != yaml.MappingNode {
		return false
	}
	if nodeAt(body, "budget") != nil {
		*notes = append(*notes, fmt.Sprintf(
			"agents.%s.budget dropped — runtimes.%s already carries a budget (the runtime's own cap wins; budgets no longer stack per agent)", profile, target))
		return false
	}
	setMapKey(body, "budget", budget)
	*notes = append(*notes, fmt.Sprintf(
		"agents.%s.budget -> runtimes.%s.budget (a budget caps execution cost on a backend; spend is now reported per runtime)", profile, target))
	return true
}

// defaultRuntimeKey names the runtimes: entry flagged default: true, or the
// sole entry when there is exactly one.
func defaultRuntimeKey(rts *yaml.Node) string {
	sole := ""
	n := 0
	for i := 0; i+1 < len(rts.Content); i += 2 {
		name, body := rts.Content[i].Value, rts.Content[i+1]
		n++
		sole = name
		if body.Kind == yaml.MappingNode && scalarAt(body, "default") == "true" {
			return name
		}
	}
	if n == 1 {
		return sole
	}
	return ""
}

// rewriteAgentRefs turns every `agent: <profile>` on a step into
// `step: <profile>`, anywhere in the tree (trigger steps, workflow steps,
// checks, nested branches). A reference to a name no profile defined is left
// alone with a note — `agent:` survives as a free-form attribution label, so
// leaving it is harmless and losing it would not be.
func rewriteAgentRefs(n *yaml.Node, defined map[string]bool, notes *[]string) {
	if n.Kind == yaml.MappingNode {
		if ref := scalarAt(n, "agent"); ref != "" {
			switch {
			case strings.Contains(ref, "{{"):
				*notes = append(*notes, fmt.Sprintf(
					"a step's agent: %q is templated — it selected a profile per run, which steps cannot do. It now reads as an attribution label only; give the step an explicit model:/step: if it needs to vary", ref))
			case defined[ref]:
				if nodeAt(n, "step") == nil {
					removeMapKey(n, "agent")
					setMapKeyFirst(n, "step", scalar(ref))
				} else {
					*notes = append(*notes, fmt.Sprintf(
						"a step names both agent: %s and step: %s — kept step:, dropped agent:", ref, scalarAt(n, "step")))
					removeMapKey(n, "agent")
				}
			}
		}
	}
	for _, c := range n.Content {
		rewriteAgentRefs(c, defined, notes)
	}
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}
