package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Step identity — the ONE stable key memory, session affinity, and outcome
// tracking all default to (docs/design/agents-removal.md §5).
//
// It replaces `agents.<name>` as the thing those three subsystems keyed off.
// The hard requirement is STABILITY ACROSS RESTARTS: a per-run UUID would wipe
// a step's track record on every boot, orphan its memories, and break session
// rebind. Nothing here reads persisted state or a clock — an identity is a
// pure function of config, recomputed identically every boot.
//
// The ladder:
//
//  1. an explicit `name:` on the step — author-pinned, stable across edits AND
//     shareable (two steps with the same name share identity, which is how a
//     migrated `agent: fixer` keeps the history it accumulated).
//  2. the STRUCTURAL id — the enclosing qualified trigger/workflow identity
//     plus the step's slot: `github.pull_request/security`.
//  3. a deterministic FINGERPRINT of the step's canonical definition, for a
//     step with no enclosing context at all.
//
// The default is (2). Editing a step's prompt must not rotate its identity;
// reordering an un-named, un-`id:`d step may.
//
// A note on `id:`. It is NOT rung 1. `id:` is the run-local handle
// `steps.<id>.outputs.*` addresses, and it is near-universal — treating it as
// a global identity would make two unrelated triggers that both write
// `id: fix` silently share one memory namespace, one session pool, and one
// track record. Instead `id:` supplies the SLOT of the structural id, which
// makes structural identity stable across reordering wherever ids are used
// while keeping each trigger's steps distinct.

// IdentityScope is the enclosing context a step's structural identity is
// built from: a qualified trigger address, a workflow name, or a check name.
type IdentityScope struct {
	// Kind is "trigger", "workflow", or "check".
	Kind string
	// Name is the enclosing entity's stable address — a trigger's qualified
	// name (`github.pull_request`, `review#a1b2c3d4`), a workflow name, or a
	// check name.
	Name string
}

// TriggerScope is the identity scope of a trigger.
func TriggerScope(name string) IdentityScope { return IdentityScope{Kind: "trigger", Name: name} }

// WorkflowScope is the identity scope of a named workflow.
func WorkflowScope(name string) IdentityScope { return IdentityScope{Kind: "workflow", Name: name} }

// CheckScope is the identity scope of a named check.
func CheckScope(name string) IdentityScope { return IdentityScope{Kind: "check", Name: name} }

// Empty reports a scope with nothing to anchor to.
func (s IdentityScope) Empty() bool { return strings.TrimSpace(s.Name) == "" }

// String renders the scope as the structural id's prefix.
func (s IdentityScope) String() string {
	if s.Empty() {
		return ""
	}
	if s.Kind == "trigger" {
		// A trigger's qualified name is already unambiguous
		// (`github.pull_request`), so it needs no kind prefix — which keeps the
		// common identity short and readable in audit rows.
		return s.Name
	}
	return s.Kind + ":" + s.Name
}

// Identity resolves this step's stable identity within an enclosing scope,
// at position slot (0-based) in its step list. See the file header for the
// ladder and why `id:` is the slot rather than the pin.
func (s Step) Identity(scope IdentityScope, slot int) string {
	// A `steps:` TEMPLATE is already a named thing: its map key IS its
	// identity, which is what makes every step extending it share one
	// memory namespace, session pool, and track record.
	if scope.Kind == "step" && strings.TrimSpace(s.Name) == "" {
		return scope.Name
	}
	return IdentityFor(scope, s.Name, s.slotLabel(slot), s.Fingerprint)
}

// IdentityFor is the ladder in its most general form, for callers that
// already computed a step's slot label (the flow runner's stepID) and do not
// want to recompute it. fingerprint is called only when the ladder reaches
// rung 3, so the hash is not paid for on the common path.
func IdentityFor(scope IdentityScope, name, slotLabel string, fingerprint func() string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n // 1. author-pinned
	}
	if prefix := scope.String(); prefix != "" {
		return prefix + "/" + slotLabel // 2. structural
	}
	if fingerprint == nil {
		return ""
	}
	return "step:" + fingerprint() // 3. deterministic fingerprint
}

// slotLabel is the step's position within its list. It delegates to
// StepSlot so that the slot an identity is built from and the slot a step
// REFERENCE addresses are the same string — see stepref.go. Two spellings
// of "which step is this" is how a session gets bound under one key and
// evicted under another.
func (s Step) slotLabel(slot int) string { return StepSlot(s, slot) }

// Fingerprint is a deterministic hash of the step's canonical definition —
// the automatic floor of the identity ladder. It is a pure function of
// config: the step is marshalled through YAML (which sorts a map's keys), so
// the same definition fingerprints identically on every boot and on every
// box, with no persisted state and no clock.
func (s Step) Fingerprint() string {
	b, err := yaml.Marshal(s)
	if err != nil {
		// Marshal of a plain struct cannot realistically fail; fall back to a
		// hash of the printed form rather than returning an empty identity
		// (which would collide every un-scoped step onto one key).
		b = []byte(fmt.Sprintf("%#v", s))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// StepIdentities returns each step's identity within a scope, in list order —
// used by validation and by the migration to check that a config's identities
// are what the author expects.
func StepIdentities(scope IdentityScope, steps []Step) []string {
	out := make([]string, 0, len(steps))
	for i, s := range steps {
		out = append(out, s.Identity(scope, i))
	}
	return out
}

// BranchScope is the identity scope INSIDE one branch of a `parallel:`
// step. Both the parent step's slot and the branch index fold in, because
// neither alone is enough: without the branch index, same-position steps
// in different branches collide; without the parent slot, branch 0 of two
// different parallel steps in one workflow collide.
//
// Every path that computes an identity has to use this — the config walk
// and the runner both — or a branch step binds a session under one key and
// has it swept under another.
func BranchScope(parent IdentityScope, parentSlot string, branch int) IdentityScope {
	name := parent.Name
	if name != "" {
		name += "/"
	}
	return IdentityScope{Kind: parent.Kind, Name: fmt.Sprintf("%s%s[%d]", name, parentSlot, branch)}
}

// ScopeForTrigger is a trigger's identity scope: its Name when it has one
// (the map form always sets it), else its `on:` plus position — so a
// list-form trigger with no name still gets a stable prefix.
func ScopeForTrigger(t TriggerSpec, index int) IdentityScope {
	if n := strings.TrimSpace(t.Name); n != "" {
		return TriggerScope(n)
	}
	if t.On != "" {
		return TriggerScope(fmt.Sprintf("%s[%d]", t.On, index))
	}
	return IdentityScope{}
}
