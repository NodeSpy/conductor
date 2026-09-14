package config

import (
	"fmt"

	"strconv"
	"strings"
)

// Addressing one step (docs/design/anchors-reuse.md).
//
// There is no top-level registry of named steps. A step lives where it runs
// — in a workflow's `steps:` list — and everything that needs to point at
// one (a `team:` role, a pack overlay) addresses it there:
//
//	review-flow/architect     by the step's own id: (or name: if it has no id)
//	review-flow[2]            by position, for a step that carries neither
//
// The container is a `workflows:` entry, or a NAMED trigger — a trigger's
// steps are steps like any other, and a pack that ships only triggers would
// otherwise have nothing a consumer could override.
//
// That is deliberately the same slot the identity ladder uses: id if
// present, else index. A reference and an identity therefore name the same
// thing by the same rule, so `review-flow/architect` addresses exactly the
// step whose structural identity is `workflow:review-flow/architect`.
//
// The index form is the escape hatch, not the habit. It is positional, so
// inserting a step above shifts it — which is why every error that can
// suggest an id says so.

// StepRef is a parsed step address.
type StepRef struct {
	// Workflow names the enclosing container: a `workflows:` entry, or a
	// named trigger.
	Workflow string
	// Slot is the step's id/name. Empty when the reference is positional.
	Slot string
	// Index is the position when Slot is empty; -1 otherwise.
	Index int
}

// String re-renders the reference in the form it was written.
func (r StepRef) String() string {
	if r.Slot != "" {
		return r.Workflow + "/" + r.Slot
	}
	return fmt.Sprintf("%s[%d]", r.Workflow, r.Index)
}

// ParseStepRef reads `<workflow>/<slot>` or `<workflow>[<index>]`.
func ParseStepRef(ref string) (StepRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return StepRef{}, fmt.Errorf("empty step reference")
	}
	if strings.HasSuffix(ref, "]") {
		open := strings.LastIndex(ref, "[")
		if open <= 0 {
			return StepRef{}, fmt.Errorf("step reference %q: malformed index — write <workflow>[<n>]", ref)
		}
		n, err := strconv.Atoi(ref[open+1 : len(ref)-1])
		if err != nil || n < 0 {
			return StepRef{}, fmt.Errorf("step reference %q: the index must be a non-negative whole number", ref)
		}
		return StepRef{Workflow: ref[:open], Index: n}, nil
	}
	// A pack namespaces its members (`review/review-flow`), so the WORKFLOW
	// part may itself contain a slash. The last one separates the slot.
	i := strings.LastIndex(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return StepRef{}, fmt.Errorf("step reference %q: write <workflow>/<step-id> (or <workflow>[<n>] for a step with no id:)", ref)
	}
	return StepRef{Workflow: ref[:i], Slot: ref[i+1:], Index: -1}, nil
}

// FindStepRef resolves a reference to the step it addresses, returning a
// POINTER into the config so a caller can read the live value.
//
// Only top-level steps of a workflow are addressable. A branch of a
// `parallel:` or a `compensate:` is an implementation detail of its parent
// and gets no stable address — pull it out into its own workflow if
// something needs to point at it.
func (c *Config) FindStepRef(ref string) (*Step, error) {
	r, err := ParseStepRef(ref)
	if err != nil {
		return nil, err
	}
	if wf, ok := c.Workflows[r.Workflow]; ok {
		return findStepIn(ref, r, "workflow", wf.Steps)
	}
	for i := range c.Triggers {
		if c.Triggers[i].Name == r.Workflow {
			return findStepIn(ref, r, "trigger", c.Triggers[i].Steps)
		}
	}
	return nil, fmt.Errorf("step reference %q: no workflow or named trigger called %q (workflows: %s)", ref, r.Workflow, c.workflowNames())
}

// FindStepIdentity resolves a reference to the step it addresses AND that
// step's identity — the key its memory, session pool, and track record use.
//
// A caller that dispatches through a reference needs both, and needs them to
// agree: binding a session under one key and evicting it under another
// leaves the session stranded. Resolving them together is what keeps them
// from drifting.
func (c *Config) FindStepIdentity(ref string) (*Step, string, error) {
	r, err := ParseStepRef(ref)
	if err != nil {
		return nil, "", err
	}
	s, err := c.FindStepRef(ref)
	if err != nil {
		return nil, "", err
	}
	scope, slot := WorkflowScope(r.Workflow), stepIndexIn(c.Workflows[r.Workflow].Steps, s)
	if _, isWorkflow := c.Workflows[r.Workflow]; !isWorkflow {
		for i := range c.Triggers {
			if c.Triggers[i].Name == r.Workflow {
				scope, slot = ScopeForTrigger(c.Triggers[i], i), stepIndexIn(c.Triggers[i].Steps, s)
				break
			}
		}
	}
	return s, s.Identity(scope, slot), nil
}

// stepIndexIn locates a resolved step by pointer identity, so the slot used
// for its structural identity is the one it actually occupies.
func stepIndexIn(steps []Step, target *Step) int {
	for i := range steps {
		if &steps[i] == target {
			return i
		}
	}
	return 0
}

// findStepIn resolves the slot half of a reference against one step list.
func findStepIn(ref string, r StepRef, kind string, steps []Step) (*Step, error) {
	if r.Slot == "" {
		if r.Index >= len(steps) {
			return nil, fmt.Errorf("step reference %q: %s %q has %d step(s), so index %d is out of range", ref, kind, r.Workflow, len(steps), r.Index)
		}
		return &steps[r.Index], nil
	}
	for i := range steps {
		if StepSlot(steps[i], i) == r.Slot {
			return &steps[i], nil
		}
	}
	return nil, fmt.Errorf("step reference %q: %s %q has no step with id/name %q (it has: %s)",
		ref, kind, r.Workflow, r.Slot, stepSlots(steps))
}

// StepSlot is a step's addressable slot within its list: its `id:`, else its
// `name:`, else its position. The identity ladder uses the same rule, so a
// reference and a structural identity always agree.
func StepSlot(s Step, i int) string {
	if id := strings.TrimSpace(s.ID); id != "" {
		return id
	}
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	return strconv.Itoa(i)
}

// stepSlots lists a workflow's addressable slots for an error message.
func stepSlots(steps []Step) string {
	if len(steps) == 0 {
		return "none — the workflow has no steps"
	}
	out := make([]string, 0, len(steps))
	for i := range steps {
		out = append(out, StepSlot(steps[i], i))
	}
	return strings.Join(out, ", ")
}

// WalkPackSteps visits every step a pack manifest ships — the steps of each
// workflow and of each shipped trigger, recursing into parallel branches and
// compensations — with a label naming where it sits.
//
// The visitor gets a POINTER into the manifest, so a pass (the skill
// boundary, the ref rewriter) can narrow a grant in place. Order is
// deterministic so a pack's warnings read the same on every load.
func (m *PackManifest) WalkPackSteps(fn func(where string, s *Step)) {
	for _, name := range sortedNames(m.Workflows) {
		wf := m.Workflows[name]
		for i := range wf.Steps {
			walkPackStep(name+"/"+StepSlot(wf.Steps[i], i), &wf.Steps[i], fn)
		}
	}
	for ti := range m.Triggers {
		t := &m.Triggers[ti]
		label := t.Name
		if label == "" {
			label = fmt.Sprintf("triggers[%d]", ti)
		}
		for i := range t.Steps {
			walkPackStep(label+"/"+StepSlot(t.Steps[i], i), &t.Steps[i], fn)
		}
	}
	for _, name := range sortedNames(m.Checks) {
		s := m.Checks[name]
		walkPackStep("checks."+name, &s, fn)
		m.Checks[name] = s
	}
}

func walkPackStep(where string, s *Step, fn func(string, *Step)) {
	fn(where, s)
	if s.Parallel != nil {
		for bi := range s.Parallel.Branches {
			for si := range s.Parallel.Branches[bi] {
				walkPackStep(fmt.Sprintf("%s branch %d[%d]", where, bi+1, si), &s.Parallel.Branches[bi][si], fn)
			}
		}
	}
	if s.Compensate != nil {
		walkPackStep(where+" compensate", s.Compensate, fn)
	}
}

// FindPackStep resolves a step reference against a manifest's own workflows,
// in the pack's un-namespaced vocabulary — what a consumer writes in
// `packs.<name>.steps:` and what `exports.steps:` lists.
func (m *PackManifest) FindPackStep(ref string) (*Step, error) {
	r, err := ParseStepRef(ref)
	if err != nil {
		return nil, err
	}
	if wf, ok := m.Workflows[r.Workflow]; ok {
		return findStepIn(ref, r, "workflow", wf.Steps)
	}
	for i := range m.Triggers {
		if m.Triggers[i].Name == r.Workflow {
			return findStepIn(ref, r, "trigger", m.Triggers[i].Steps)
		}
	}
	return nil, fmt.Errorf("step reference %q: this pack ships no workflow or named trigger called %q (workflows: %s)",
		ref, r.Workflow, sortedJoin(mapKeys(m.Workflows)))
}

// NamespaceStepRef scopes the WORKFLOW half of a step reference under a pack
// instance, leaving the slot alone: `review-flow/review` -> `review/review-flow/review`.
func NamespaceStepRef(ns, ref string) string {
	r, err := ParseStepRef(ref)
	if err != nil {
		return ref // left as written; the resolver reports it
	}
	r.Workflow = ns + "/" + r.Workflow
	return r.String()
}
