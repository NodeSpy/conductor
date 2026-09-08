package config

import (
	"fmt"
	"reflect"
)

// extends.go implements Docker-Compose-style `extends:` inheritance for the
// map-of-named-entries config sections (agents, runtimes, workflows, handoffs).
// A child entry names one parent in the SAME section and inherits every field it
// leaves unset. Resolution runs once in Load, after imports merge and before
// validation, so downstream only ever sees fully-resolved entries.
//
// Merge rules (see mergeStruct): scalars — child wins when set; pointers —
// child wins when non-nil; maps (labels/env/inputs) — deep-merged, child keys
// override; slices — child replaces when non-empty. Guidance is the one field
// that STACKS (parent parts under child parts) instead of replacing — see
// mergeGuidance. Chains resolve root→leaf; cycles and unknown targets are load
// errors. Documented limit: a non-pointer bool inherits only when the child
// leaves it false (it can't force a parent's true back to false).

var guidanceSpecPtrType = reflect.TypeOf((*GuidanceSpec)(nil))

// resolveExtends resolves `extends:` across every section that supports it.
// Connectors/stores/vaults are intentionally excluded — they decode via a
// retained raw yaml.Node, which needs a different (node-level) merge.
func (c *Config) resolveExtends() error {
	if err := resolveExtendsSection(c.Agents, "agent", func(a AgentProfile) string { return a.Extends }); err != nil {
		return err
	}
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
