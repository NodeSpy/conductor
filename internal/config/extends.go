package config

import (
	"fmt"
	"reflect"
	"strings"
)

// extends.go implements Docker-Compose-style `extends:` inheritance for the
// map-of-named-entries config sections (runtimes, workflows, handoffs) and for
// triggers. A child entry names one parent in the SAME section and inherits
// every field it leaves unset. Resolution runs once in Load, after imports
// merge and before validation, so downstream only ever sees fully-resolved
// entries.
//
// Steps are NOT in that list. Reuse of step behavior is plain YAML anchors
// (anchors.go) — `&base` / `<<: *base` — which needs no conductor machinery
// and works the same in a pack manifest. What remains of the top-level
// `steps:` map is a REGISTRY of named steps that other config addresses by
// name (a `team:` role, a pack's step roles); resolveRoleStep below is that
// lookup, and it is not reachable from YAML.
//
// Merge rules (see mergeStruct): scalars — child wins when set; pointers —
// child wins when non-nil; maps (labels/env/inputs) — deep-merged, child keys
// override; slices — child replaces when non-empty. Guidance is the one field
// that STACKS (parent parts under child parts) instead of replacing — see
// mergeGuidance. Chains resolve root→leaf; cycles and unknown targets are load
// errors. Documented limit: a non-pointer bool inherits only when the child
// leaves it false (it can't force a parent's true back to false).

var guidanceSpecPtrType = reflect.TypeOf((*GuidanceSpec)(nil))

// ResolveExtends is resolveExtends for callers that build a Config without
// going through Load (the connectors-model lowering, test rigs).
func (c *Config) ResolveExtends() error { return c.resolveExtends() }

// resolveExtends resolves `extends:` across every section that supports it.
// Connectors/stores/vaults are intentionally excluded — they decode via a
// retained raw yaml.Node, which needs a different (node-level) merge.
func (c *Config) resolveExtends() error {
	if err := resolveExtendsSection(c.Runtimes, "runtime", func(r RuntimeConfig) string { return r.Extends }); err != nil {
		return err
	}
	if err := resolveExtendsSection(c.Workflows, "workflow", func(w WorkflowDef) string { return w.Extends }); err != nil {
		return err
	}
	if err := resolveExtendsSection(c.Handoffs, "handoff", func(h HandoffConfig) string { return h.Extends }); err != nil {
		return err
	}
	return c.resolveStepRefs()
}

// resolveStepRefs resolves every `step: <name>` reference in the config
// against the top-level `steps:` registry. It runs after packs instantiate,
// so a pack's role name has already been namespaced or rebound to whatever
// the consumer pointed it at.
func (c *Config) resolveStepRefs() error {
	var err error
	c.WalkSteps(func(scope IdentityScope, _ int, s *Step) {
		if err != nil || s.StepRef == "" {
			return
		}
		// A registry entry may not play another one. Chains would need an
		// order the map does not have, and an entry that wants another's
		// fields is describing an anchor, not a role.
		if scope.Kind == "step" {
			err = fmt.Errorf("config: steps.%s: a named step cannot itself use `step: %s` — share fields with a YAML anchor instead", scope.Name, s.StepRef)
			return
		}
		err = c.ResolveStepRef(s.StepRef, s)
	})
	return err
}

// ResolveStepRefsIn resolves `step:` across a step list built OUTSIDE Load —
// a lowered legacy trigger, an agent-authored plan — recursing into the
// nested step forms. Idempotent, so calling it on a load-resolved list does
// nothing.
func (c *Config) ResolveStepRefsIn(steps []Step) error {
	for i := range steps {
		if err := c.resolveStepRefIn(&steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) resolveStepRefIn(s *Step) error {
	if s.StepRef != "" {
		if err := c.ResolveStepRef(s.StepRef, s); err != nil {
			return err
		}
	}
	if s.Parallel != nil {
		for bi := range s.Parallel.Branches {
			if err := c.ResolveStepRefsIn(s.Parallel.Branches[bi]); err != nil {
				return err
			}
		}
	}
	if s.Compensate != nil {
		return c.resolveStepRefIn(s.Compensate)
	}
	return nil
}

// ResolveStepRef fills a step in from the named `steps:` entry it plays.
//
// Two callers: the load-time pass above, and the runtime, which synthesizes
// team role steps from names a `team:` block supplies. Fields already set
// win; the rest come from the registry entry, guidance stacking as it does
// under `extends:`.
//
// Identity comes with it. A step that pins no `name:` takes the registry
// key, so every team using `architect` as its planner — and every workflow
// step that plays it — share one memory namespace, session pool, and track
// record. That sharing is the reason to name a step at all; a step that
// only wants the fields should use an anchor and stay anonymous.
//
// Applying twice is a no-op: the reference is cleared once merged, which
// matters because guidance stacks rather than fills.
func (c *Config) ResolveStepRef(name string, s *Step) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	base, ok := c.Steps[name]
	if !ok {
		return fmt.Errorf("config: step: %q names no entry in the top-level steps: map (defined: %s)", name, sortedKeys(c.Steps))
	}
	pinned := strings.TrimSpace(s.Name)
	mergeStruct(reflect.ValueOf(s).Elem(), reflect.ValueOf(base))
	if pinned == "" {
		s.Name = name
	} else {
		s.Name = pinned
	}
	s.StepRef = ""
	return nil
}

// resolveExtendsSection resolves the extends chain for one map section,
// memoizing resolved entries and rejecting cycles and unknown parents.
func resolveExtendsSection[T any](m map[string]T, kind string, extendsOf func(T) string) error {
	if len(m) == 0 {
		return nil
	}
	resolved := map[string]T{}
	var resolve func(name string, path []string) (T, error)
	resolve = func(name string, path []string) (T, error) {
		if r, ok := resolved[name]; ok {
			return r, nil
		}
		cur := m[name] // name is always a real key here (callers iterate m or validate first)
		parent := extendsOf(cur)
		if parent == "" {
			resolved[name] = cur
			return cur, nil
		}
		for _, p := range path {
			if p == name {
				var z T
				return z, fmt.Errorf("config: %s %q: extends: cycle (%v -> %s)", kind, name, path, name)
			}
		}
		if _, ok := m[parent]; !ok {
			var z T
			return z, fmt.Errorf("config: %s %q: extends: unknown %s %q", kind, name, kind, parent)
		}
		base, err := resolve(parent, append(path, name))
		if err != nil {
			return base, err
		}
		merged := cur
		mergeStruct(reflect.ValueOf(&merged).Elem(), reflect.ValueOf(base))
		resolved[name] = merged
		return merged, nil
	}
	for name := range m {
		if _, err := resolve(name, nil); err != nil {
			return err
		}
	}
	for name, v := range resolved {
		m[name] = v
	}
	return nil
}

// resolveTriggerExtends resolves `extends:` across the triggers list (keyed by
// Name) and strips abstract bases. It runs BEFORE NormalizeTriggers so a child
// can inherit an abstract base's `on:` (and so bases with no `on:` never reach
// the on:-required / manual-name checks). Filters/options deep-merge and
// steps/hooks replace via mergeStruct; Name/Abstract/Extends are never
// inherited (they are identity, not config).
func (c *Config) resolveTriggerExtends() error {
	ts := c.Triggers
	if len(ts) == 0 {
		return nil
	}
	byName := map[string][]int{}
	for i := range ts {
		if ts[i].Name != "" {
			byName[ts[i].Name] = append(byName[ts[i].Name], i)
		}
	}
	const (
		unvisited = iota
		visiting
		done
	)
	state := make([]int, len(ts))
	var resolve func(i int) error
	resolve = func(i int) error {
		switch state[i] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("config: trigger %s: extends: cycle", triggerRef(ts[i], i))
		}
		state[i] = visiting
		if ext := ts[i].Extends; ext != "" {
			js := byName[ext]
			switch {
			case len(js) == 0:
				return fmt.Errorf("config: trigger %s: extends: unknown trigger %q", triggerRef(ts[i], i), ext)
			case len(js) > 1:
				return fmt.Errorf("config: trigger %s: extends: %q is ambiguous (%d triggers share that name)", triggerRef(ts[i], i), ext, len(js))
			}
			j := js[0]
			if j == i {
				return fmt.Errorf("config: trigger %s: extends: itself", triggerRef(ts[i], i))
			}
			if err := resolve(j); err != nil {
				return err
			}
			name, abstract, ext := ts[i].Name, ts[i].Abstract, ts[i].Extends
			merged := ts[i]
			mergeStruct(reflect.ValueOf(&merged).Elem(), reflect.ValueOf(ts[j]))
			merged.Name, merged.Abstract, merged.Extends = name, abstract, ext
			ts[i] = merged
		}
		state[i] = done
		return nil
	}
	for i := range ts {
		if err := resolve(i); err != nil {
			return err
		}
	}
	// Drop abstract bases — they exist only to be extended.
	out := make([]TriggerSpec, 0, len(ts))
	for i, t := range ts {
		if t.Abstract {
			if t.Manual() {
				return fmt.Errorf("config: trigger %s: an abstract base cannot be `on: manual` (it never fires and is not a `conductor run` target)", triggerRef(t, i))
			}
			continue
		}
		out = append(out, t)
	}
	c.Triggers = out
	return nil
}

// triggerRef labels a trigger for errors: its name, else its list position.
func triggerRef(t TriggerSpec, i int) string {
	if t.Name != "" {
		return fmt.Sprintf("%q", t.Name)
	}
	return fmt.Sprintf("triggers[%d]", i)
}

// mergeStruct fills dst's unset fields from src (the resolved parent). dst must
// be an addressable struct value; src the same type. See the file header for
// the per-kind policy.
func mergeStruct(dst, src reflect.Value) {
	for i := 0; i < dst.NumField(); i++ {
		df, sf := dst.Field(i), src.Field(i)
		if !df.CanSet() {
			continue
		}
		// Guidance stacks (parent under child) rather than filling if-unset.
		if df.Type() == guidanceSpecPtrType {
			mergeGuidance(df, sf)
			continue
		}
		switch df.Kind() {
		case reflect.Pointer, reflect.Interface:
			if df.IsNil() && !sf.IsNil() {
				df.Set(sf)
			}
		case reflect.Map:
			if sf.IsNil() {
				continue
			}
			if df.IsNil() {
				df.Set(sf)
				continue
			}
			// Deep-merge: keep child's keys, add the parent's missing ones.
			for _, k := range sf.MapKeys() {
				if !df.MapIndex(k).IsValid() {
					df.SetMapIndex(k, sf.MapIndex(k))
				}
			}
		case reflect.Slice:
			if df.Len() == 0 && sf.Len() > 0 {
				df.Set(sf)
			}
		default: // basic scalars (string, int, bool, Duration): inherit when zero
			if df.IsZero() && !sf.IsZero() {
				df.Set(sf)
			}
		}
	}
}

// mergeGuidance stacks a parent GuidanceSpec under a child's: the child inherits
// the parent's parts beneath its own unless it resets with { replace }. A child
// with no guidance inherits the parent's wholesale.
func mergeGuidance(dst, src reflect.Value) {
	if src.IsNil() {
		return
	}
	if dst.IsNil() {
		dst.Set(src)
		return
	}
	child := dst.Interface().(*GuidanceSpec)
	if child.Replace {
		return // child resets — does not inherit the parent's parts
	}
	parent := src.Interface().(*GuidanceSpec)
	merged := child.prepend(parent.Parts)
	dst.Set(reflect.ValueOf(&merged))
}
