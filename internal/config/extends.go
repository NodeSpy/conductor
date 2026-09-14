package config

import (
	"fmt"
	"reflect"
)

// extends.go implements Docker-Compose-style `extends:` inheritance for the
// map-of-named-entries config sections (runtimes, workflows, handoffs) and for
// triggers. A child entry names one parent in the SAME section and inherits
// every field it leaves unset. Resolution runs once in Load, after imports
// merge and before validation, so downstream only ever sees fully-resolved
// entries.
//
// A STEP's `extends:` is a different thing that happens to share the word,
// and it lives in stepmerge.go. It takes an anchor alias or an inline map
// rather than a sibling key (there is no registry of steps), and it merges
// the YAML NODES rather than the decoded values — which it must, because
// its `!override` / `!reset` escape hatches are node TAGS and are gone by
// the time reflection sees a struct. The two cannot share a mechanism;
// they only share a policy, and where they overlap (scalars fill, maps
// deep-merge, guidance stacks) both are written to agree.
//
// Steps also have `<<: *base` (anchors.go), which is plain YAML: dumb
// override, resolved by the parser. See stepmerge.go for why both exist.
// Where something must POINT at a particular step (a `team:` role, a pack
// overlay), it addresses it where it lives: `<workflow>/<id>` or
// `<workflow>[<n>]`. See stepref.go.
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

// MergeStepInto fills dst's unset fields from base, using the same merge
// policy as `extends:` — scalars fill, maps deep-merge, slices replace when
// dst leaves them empty, and guidance STACKS (base's tone under dst's).
//
// Exported for the runtime, which synthesizes a team's role steps from the
// workflow step the team references and has to join the two.
func MergeStepInto(dst *Step, base Step) {
	mergeStruct(reflect.ValueOf(dst).Elem(), reflect.ValueOf(base))
}
