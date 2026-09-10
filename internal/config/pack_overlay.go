package config

import (
	"fmt"
	"reflect"
	"strings"
)

// The mirrored-section overlay (docs/design/runtimes-models-packs.md §5.3).
//
// The `packs.<name>:` block MIRRORS the pack's own sections, and keys
// deep-merge onto the pack's members BY NAME using the machinery that
// already exists (`extends:`-style mergeStruct, additive guidance). There is
// no pack-specific override language to learn:
//
//	packs:
//	  pr-review-team:
//	    on:                                       # its triggers, by qualified name
//	      github.pull_request: { filters: { labels_not: [wip] } }   # ADD a filter
//	      gitlab.merge_request: { enabled: false }                  # turn one off
//	    steps:                                    # its steps, by name
//	      security:  { guidance: "focus on authz + SSRF" }          # ADDITIVE
//	      summarize: { enabled: false }
//	    models:                                   # its fleets, by name
//	      reviewer: claude-opus-5
//
// Only NAMED members are addressable — a pack must name what it wants a
// consumer to be able to reach. An overlay key that matches nothing is an
// error rather than silent dead config: the whole point is targeted
// override, and a typo that quietly does nothing is the worst outcome.

// applyTriggerOverlay deep-merges the instance's `on:` overlay onto the
// pack's shipped triggers, addressed by their qualified name.
//
// It runs alongside the existing `triggers:` arming block (which is the
// consent surface — enabled + repos). `on:` is the behavior surface: filters,
// policy, and disabling. Both use TriggerArm, so the merge semantics are
// identical and there is one thing to learn.
func (st *packInstantiation) applyTriggerOverlay(ns string, inst PackInstance, trs []TriggerSpec, armName func(int) string) error {
	if len(inst.On) == 0 {
		return nil
	}
	matched := map[string]bool{}
	for i := range trs {
		name := armName(i)
		ov, ok := inst.On[name]
		if !ok {
			continue
		}
		matched[name] = true
		if err := applyTriggerArm(&trs[i], ov); err != nil {
			return fmt.Errorf("pack %q: on: %s: %w", ns, name, err)
		}
	}
	for name := range inst.On {
		if !matched[name] {
			return fmt.Errorf("pack %q: on: %q addresses no trigger this pack ships (shipped: %s)",
				ns, name, triggerNames(trs))
		}
	}
	return nil
}

// applyStepOverlay deep-merges the instance's `steps:` overlay onto the
// pack's shipped step templates, by name.
//
// The override semantics are the OVERRIDE half of Binding (a map deep-merges
// onto the bundle), so this is the same surface a role binding already uses
// — the difference is only that the overlay reaches members the pack did not
// declare as roles.
//
// `enabled: false` disables a step: it is lowered to an `if: "false"` so the
// runner skips it, which is how a step is turned off without restructuring
// the pack's step list.
func (st *packInstantiation) applyStepOverlay(ns string, inst PackInstance, man *PackManifest) error {
	for _, ref := range sortedNames(inst.Steps) {
		b := inst.Steps[ref]
		if b.IsBind() {
			return fmt.Errorf("pack %q: steps: %q: the bind form (`%s: %s`) is gone — there is no top-level steps: registry to name. Write an override instead, and reach your own config with a YAML anchor if you want to reuse it: steps: { %s: { <<: *%s } }",
				ns, ref, ref, b.Bind, ref, b.Bind)
		}
		if !b.IsOverride() {
			continue
		}
		target, err := man.FindPackStep(ref)
		if err != nil {
			return fmt.Errorf("pack %q: steps: %w", ns, err)
		}
		base := *target
		over := b.Override
		// `enabled: false` is the disable spelling; it is not a Step field,
		// so lower it before the struct merge sees it.
		disabled := false
		if v, has := over["enabled"]; has {
			en, isBool := v.(bool)
			if !isBool {
				return fmt.Errorf("pack %q: steps.%s.enabled must be true or false", ns, ref)
			}
			disabled = !en
			over = withoutKey(over, "enabled")
		}
		merged, err := applyStepOverride(base, over)
		if err != nil {
			return fmt.Errorf("pack %q: steps.%s override: %w", ns, ref, err)
		}
		// Guidance is ADDITIVE: the consumer's line stacks under/over the
		// pack's rather than replacing it, matching the extends: contract.
		merged.Guidance = stackGuidance(base.Guidance, merged.Guidance)
		if disabled {
			merged.If = "false"
		}
		*target = merged
	}
	return nil
}

// stackGuidance appends the consumer's parts after the pack's, unless the
// consumer asked to replace. applyStepOverride's YAML round-trip replaces
// the whole block, so the stacking is restored here.
func stackGuidance(base, over *GuidanceSpec) *GuidanceSpec {
	switch {
	case over == nil:
		return base
	case base == nil || over.Replace:
		return over
	}
	if reflect.DeepEqual(base.Parts, over.Parts) {
		return over
	}
	stacked := over.prepend(base.Parts)
	return &stacked
}

// applyFleetOverlay replaces a pack's named fleet with the consumer's
// choice — rung 1 of the model resolution ladder (§2.3).
func (st *packInstantiation) applyFleetOverlay(ns string, inst PackInstance, fleets map[string]FleetSpec) error {
	for _, name := range sortedNames(inst.Models) {
		if _, ok := fleets[name]; !ok {
			return fmt.Errorf("pack %q: models: %q addresses no fleet this pack ships (shipped: %s)",
				ns, name, sortedJoin(mapKeys(fleets)))
		}
		fleets[name] = inst.Models[name]
	}
	return nil
}

func withoutKey(m map[string]any, key string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// triggerOverlayName is a shipped trigger's addressable name — what a
// consumer writes as an `on:` overlay key. It is the trigger's own qualified
// name before namespacing.
func triggerOverlayName(t TriggerSpec) string {
	if n := strings.TrimSpace(t.Name); n != "" {
		return n
	}
	return t.On
}
