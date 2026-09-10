package config

import (
	"fmt"
	"reflect"
	"strings"
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
	if err := resolveExtendsSection(c.Steps, "step", func(s Step) string { return s.Extends }); err != nil {
		return err
	}
	if err := c.resolveStepTemplates(); err != nil {
		return err
	}
	return nil
}

// ApplyStepTemplates resolves `extends:` for a step list built OUTSIDE Load
// (a lowered trigger, a test rig, an agent-authored plan). It is idempotent:
// a step whose template is already merged is skipped, which matters because
// guidance stacks rather than filling.
func (c *Config) ApplyStepTemplates(where string, steps []Step) error {
	for i := range steps {
		if err := c.applyStepTemplate(where, &steps[i]); err != nil {
			return err
		}
	}
	return nil
}

// resolveStepTemplates applies `extends:` from a step in a trigger, workflow,
// or check to an entry in the top-level `steps:` map — the cross-section half
// of step reuse (the same-section half, a template extending a template, is
// handled by the resolveExtendsSection call above, which runs first so a
// chain is fully collapsed before anything inherits from it).
//
// This is what carries a retired named agent profile forward: several
// triggers that all dispatched to `agent: fixer` become several steps that
// all `extends: fixer`, inheriting the behavior AND — because a template's
// key becomes the child's identity when the child pins none — the memory
// namespace, session pool, and track record that name accumulated.
func (c *Config) resolveStepTemplates() error {
	var walkErr error
	apply := func(where string, steps []Step) {
		for i := range steps {
			if walkErr != nil {
				return
			}
			walkErr = c.applyStepTemplate(where, &steps[i])
		}
	}
	for i := range c.Triggers {
		apply(triggerRef(c.Triggers[i], i), c.Triggers[i].Steps)
	}
	for name, wf := range c.Workflows {
		apply("workflow "+name, wf.Steps)
	}
	for name, ck := range c.Checks {
		s := ck
		if walkErr == nil {
			walkErr = c.applyStepTemplate("check "+name, &s)
		}
		c.Checks[name] = s
	}
	return walkErr
}

// applyStepTemplate merges one step with the template it extends, recursing
// into the nested step forms (parallel branches, compensations).
func (c *Config) applyStepTemplate(where string, s *Step) error {
	if ext := strings.TrimSpace(s.Extends); ext != "" && !s.tmplApplied {
		base, ok := c.Steps[ext]
		if !ok {
			return fmt.Errorf("config: %s: extends: unknown step template %q (defined: %s)", where, ext, sortedKeys(c.Steps))
		}
		// Identity: a child that pins no name inherits the TEMPLATE'S name —
		// its map key — so every step extending one template shares one
		// identity. Pinning `name:` on the child opts out.
		name := s.Name
		mergeStruct(reflect.ValueOf(s).Elem(), reflect.ValueOf(base))
		if strings.TrimSpace(name) == "" {
			s.Name = ext
		} else {
			s.Name = name
		}
		s.Extends, s.tmplApplied = ext, true
	}
	if s.Parallel != nil {
		for bi := range s.Parallel.Branches {
			for si := range s.Parallel.Branches[bi] {
				if err := c.applyStepTemplate(where, &s.Parallel.Branches[bi][si]); err != nil {
					return err
				}
			}
		}
	}
	if s.Compensate != nil {
		if err := c.applyStepTemplate(where+" compensate", s.Compensate); err != nil {
			return err
		}
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
