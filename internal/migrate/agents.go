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
//	behavior fields  -> ONTO EACH STEP that referenced the profile
//	memory / session / outcome opt-ins -> the same steps
//
// There is no top-level registry to move a profile into, so the behavior is
// INLINED at every site that named it. Where a profile is referenced more
// than once IN ONE FILE, the sites share a YAML anchor parked under
// `x-migrated:` — the reuse mechanism this config surface now has. Where
// the sites are in different files, each gets its own copy: anchors do not
// cross `imports:`, and duplicated config that works beats DRY config that
// does not parse.
//
// Because `agents:` commonly sits in the main config while the triggers
// that named it sit in `conf.d/*.yaml`, the profile table is gathered from
// the WHOLE import tree before any file is rewritten (see CollectProfiles
// and AutoMigrate). A file with references but no `agents:` block of its
// own still gets them inlined.
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

// applyAgentsPass inlines each profile's behavior onto the steps that named
// it, moves each profile's budget onto its runtime, and drops `agents:`.
//
// extra carries profiles declared in OTHER files of the import tree, so a
// file holding only triggers still resolves the names they reference.
func applyAgentsPass(masked []byte, extra map[string]*yaml.Node, notes *[]string) (out []byte, changed bool, err error) {
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
	hasLocal := agents != nil && agents.Kind == yaml.MappingNode && len(agents.Content) > 0
	if !hasLocal && len(extra) == 0 {
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
	if !hasLocal {
		// This file declares no profiles, but a file it shares an import
		// tree with does; inline those onto whatever references them here.
		fragments := map[string]*yaml.Node{}
		for name, n := range extra {
			fragments[name] = n
		}
		n := inlineProfiles(root, fragments, notes)
		if removeMapKey(root, "agents") {
			n++
		}
		if n == 0 {
			return nil, false, nil
		}
		b, mErr := marshalDoc(&doc)
		if mErr != nil {
			return nil, false, mErr
		}
		return b, true, nil
	}

	templates, parent := buildProfileFragments(root, true, notes)

	// A profile that extended another is FLATTENED here: without a registry
	// there is no second entry to inherit from at load, and inlining a
	// half-built fragment would silently lose the parent's behavior.
	flattenProfileChains(templates, parent, notes)

	fragments := map[string]*yaml.Node{}
	for i := 0; i+1 < len(templates.Content); i += 2 {
		fragments[templates.Content[i].Value] = templates.Content[i+1]
	}
	for name, n := range extra {
		if _, local := fragments[name]; !local {
			fragments[name] = n
		}
	}
	removeMapKey(root, "agents")

	inlineProfiles(root, fragments, notes)

	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// buildProfileFragments turns each `agents:` entry into a ready-to-inline
// step fragment. When moveBudget is set it also relocates each profile's
// budget onto the runtime it ran on — the one part that mutates the rest of
// the document, and so the one part CollectProfiles skips.
func buildProfileFragments(root *yaml.Node, moveBudget bool, notes *[]string) (*yaml.Node, map[string]string) {
	agents := mapValue(root, "agents")
	if agents == nil || agents.Kind != yaml.MappingNode {
		return nil, nil
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
				"agents.%s: provider: %s named a backend, not a model — the steps that referenced it now BARE LAUNCH (no --model, the runtime's own default). Pin one with model:, or declare a fleet under models:", name, provider))
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
		if b := nodeAt(body, "budget"); b != nil && moveBudget {
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
	}
	sort.Strings(names)
	return templates, parent
}

// CollectProfiles reads one file's `agents:` block into ready-to-inline step
// fragments, so AutoMigrate can gather the whole import tree before
// rewriting any single file. Returns nil when the file declares none.
func CollectProfiles(raw []byte) map[string]*yaml.Node {
	var doc yaml.Node
	if err := yaml.Unmarshal(maskEnv(raw), &doc); err != nil {
		return nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	var notes []string
	templates, parent := buildProfileFragments(doc.Content[0], false, &notes)
	if templates == nil || len(templates.Content) == 0 {
		return nil
	}
	flattenProfileChains(templates, parent, &notes)
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(templates.Content); i += 2 {
		out[templates.Content[i].Value] = templates.Content[i+1]
	}
	return out
}

// inlineProfiles rewrites every `agent: <name>` in the tree that a fragment
// answers, replacing it with that profile's fields.
//
// A profile referenced ONCE in this file is copied straight onto the step. A
// profile referenced more than once gets an anchor under `x-migrated:` and
// each site merges it with `<<:` — same behavior, one definition, and the
// idiom the config surface now uses for reuse. Either way the step carries
// `name: <old-agent-name>`, which is what keeps its memory namespace,
// session pool, and track record pointing at the history it already has.
//
// Returns the number of sites rewritten.
func inlineProfiles(root *yaml.Node, fragments map[string]*yaml.Node, notes *[]string) int {
	sites := map[string][]*yaml.Node{}
	collectAgentSites(root, fragments, &sites, notes)
	for name := range fragments {
		if len(sites[name]) == 0 {
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s dropped — no step referenced it, and there is no top-level steps: section left to park it in. Its behavior is in the backup file if you still want it", name))
		}
	}
	if len(sites) == 0 {
		return 0
	}
	names := make([]string, 0, len(sites))
	for n := range sites {
		names = append(names, n)
	}
	sort.Strings(names)

	// Shared fragments become anchors; the holder goes FIRST so every alias
	// follows its definition, which YAML requires.
	holder := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range names {
		if len(sites[name]) < 2 {
			continue
		}
		frag := fragments[name]
		frag.Anchor = anchorName(name)
		setMapKey(holder, name, frag)
	}
	if len(holder.Content) > 0 {
		setMapKeyFirst(root, migratedAnchorKey, holder)
	}

	count := 0
	for _, name := range names {
		frag := fragments[name]
		shared := len(sites[name]) >= 2
		for _, step := range sites[name] {
			removeMapKey(step, "agent")
			if shared {
				step.Content = append([]*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"},
					{Kind: yaml.AliasNode, Value: frag.Anchor, Alias: frag},
				}, step.Content...)
			} else {
				// Copy the fields the step does not already set — its own
				// values win, exactly as a `<<:` merge would leave them.
				for i := 0; i+1 < len(frag.Content); i += 2 {
					k, v := frag.Content[i].Value, frag.Content[i+1]
					if nodeAt(step, k) == nil {
						setMapKey(step, k, v)
					}
				}
			}
			count++
		}
		switch {
		case shared:
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s -> %s.%s, merged into %d step(s) with `<<: *%s` (name: %s pins the identity, so its memory/session/outcome history carries over)",
				name, migratedAnchorKey, name, len(sites[name]), anchorName(name), name))
		default:
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s -> inlined on the step that referenced it (name: %s pins the identity, so its memory/session/outcome history carries over)", name, name))
		}
	}
	return count
}

// migratedAnchorKey is the `x-` holder the migration parks shared fragments
// under. The loader ignores `x-`-prefixed top-level keys, so it exists only
// to give the anchors somewhere to live.
const migratedAnchorKey = "x-migrated"

// anchorName is the anchor label for a profile. Anchors may not contain the
// YAML flow indicators, so a hostile-looking name is sanitized rather than
// emitted verbatim.
func anchorName(name string) string {
	repl := strings.NewReplacer(" ", "_", ",", "_", "[", "_", "]", "_", "{", "_", "}", "_", "*", "_", "&", "_")
	return repl.Replace(name)
}

// collectAgentSites finds every step mapping whose `agent:` names a known
// profile. A templated `agent:` selected a profile per run, which a step
// cannot do; it is left alone as an attribution label, with a note.
func collectAgentSites(n *yaml.Node, fragments map[string]*yaml.Node, sites *map[string][]*yaml.Node, notes *[]string) {
	if n.Kind == yaml.MappingNode {
		if ref := scalarAt(n, "agent"); ref != "" {
			switch {
			case strings.Contains(ref, "{{"):
				*notes = append(*notes, fmt.Sprintf(
					"a step's agent: %q is templated — it selected a profile per run, which steps cannot do. It now reads as an attribution label only; give the step an explicit model:/guidance: if it needs to vary", ref))
			case fragments[ref] != nil:
				(*sites)[ref] = append((*sites)[ref], n)
			}
		}
	}
	for _, c := range n.Content {
		collectAgentSites(c, fragments, sites, notes)
	}
}

// flattenProfileChains merges each `agents.<child>.extends: <parent>` into
// the child's fragment, root-first, so every fragment is self-contained
// before anything is inlined. A missing parent or a cycle drops the link
// with a note rather than emitting a fragment that lost half its behavior
// silently.
func flattenProfileChains(templates *yaml.Node, parent map[string]string, notes *[]string) {
	if len(parent) == 0 {
		return
	}
	present := map[string]bool{}
	for i := 0; i+1 < len(templates.Content); i += 2 {
		present[templates.Content[i].Value] = true
	}
	done := map[string]bool{}
	var flatten func(name string)
	flatten = func(name string) {
		if done[name] {
			return
		}
		done[name] = true
		p, ok := parent[name]
		if !ok {
			return
		}
		switch {
		case !present[p]:
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s.extends: %s dropped — no profile named %q to inherit from", name, p, p))
			return
		case cyclic(name, parent):
			*notes = append(*notes, fmt.Sprintf(
				"agents.%s.extends: %s dropped — the inheritance chain loops back on itself", name, p))
			return
		}
		flatten(p)
		child, base := nodeAt(templates, name), nodeAt(templates, p)
		if child == nil || base == nil || child.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(base.Content); i += 2 {
			k, v := base.Content[i].Value, base.Content[i+1]
			if k == "name" {
				continue // identity is the child's own
			}
			if nodeAt(child, k) == nil {
				setMapKey(child, k, v)
			}
		}
		*notes = append(*notes, fmt.Sprintf(
			"agents.%s.extends: %s flattened into %s — there is no registry for one entry to inherit from at load, so the parent's fields are copied in", name, p, name))
	}
	for name := range parent {
		flatten(name)
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
