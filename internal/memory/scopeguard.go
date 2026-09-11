package memory

import (
	"context"
	"fmt"
	"strings"
)

// The H8 write guard was added to `remember` and stopped there. recall, list
// and forget stayed open: an agent could read any scope (including the shared
// `global` bucket it was forbidden to WRITE), list every entry on the daemon
// regardless of tenant, and forget an id belonging to someone else. Guarding
// those three the way `remember` was guarded would leave the same shape of
// hole for the next op added.
//
// CheckOp is the one gate instead. Both agent-facing faces — the `run: code`
// binding (code.memInvoke) and the `memory.*` verbs (connector.memoryImpl) —
// call it before EVERY op, and TestEveryAgentFacingMemoryOpIsGuarded fails if
// either stops.
//
// It layers two things:
//
//	the unconditional reserved-bucket rule (CheckAgentScope), which no policy
//	can open, and
//	the operator's per-scope allowlist, installed by the flow layer from
//	policy.agent_authored.allow_memory_scopes — deny-by-default, with the
//	triggering scope implicitly allowed, exactly like allow_stores.

// Caller is WHO is asking, for authorization. It is supplied per call — a
// process-wide guard cannot know which dispatch is on the other end, and
// "your own scope" is the whole point of the allowlist.
//
// AgentFacing is the trusted/untrusted split the rest of the system already
// makes: an agent-authored plan step, a skill verb call, or the agent's own
// memory tool. A CONFIG-AUTHORED `uses: memory.recall` step is not — the
// operator wrote it with their own credential, and gating it would be a
// different product.
type Caller struct {
	// Repo is the dispatch's target repo; its scope (repo:<repo>) is
	// implicitly allowed. Empty when the caller has no target.
	Repo string
	// AgentFacing marks a call the operator did not write.
	AgentFacing bool
}

// ScopeGuard authorizes one memory op against a scope, for one caller. op is
// remember|recall|list|forget; scope is "" when the caller named none.
type ScopeGuard func(c Caller, op, scope string) error

// SetScopeGuard installs the agent-facing scope allowlist. nil is ignored so
// a caller cannot clear an installed guard by passing nothing.
func (m *Manager) SetScopeGuard(g ScopeGuard) {
	if g == nil {
		return
	}
	m.scopeGuard.Store(&g)
}

// namesItsOwnScope reports whether the CALLER chose the scope for this op.
//
// Only `remember` does. That distinction is what the reserved-bucket rule is
// about: CheckAgentScope refuses an agent DIRECTING a write at the shared
// bucket by naming it. `forget` addresses an id, and its scope is whatever
// the stored entry already has — applying the reserved rule there would make
// an agent's own note (written with the default empty scope, which lands in
// global) permanently undeletable by the agent that wrote it. Ownership on
// forget is the allowlist below, which is the check that actually answers
// "is this entry yours to touch".
func namesItsOwnScope(op string) bool { return op == "remember" }

// CheckOp is the single authorization gate for an agent-facing memory op.
//
// Deny-by-default holds through the layers: an unrecognized op is refused
// rather than passed, and when a guard is installed an unscoped read is its
// to accept or refuse (the flow guard refuses one, so a narrow grant can't
// recall every tenant's entries by simply naming no scope).
func (m *Manager) CheckOp(c Caller, op, scope string) error {
	switch op {
	case "remember", "recall", "list", "forget":
	default:
		return fmt.Errorf("memory: no operation %q", op)
	}
	// The reserved bucket is refused first and unconditionally: it is
	// injected into every opted-in agent's prompt on this daemon, so it is
	// never an agent's to write, and no allowlist can open it.
	if namesItsOwnScope(op) {
		if err := CheckAgentScope(scope); err != nil {
			return err
		}
	}
	// The operator's allowlist applies to AGENT-FACING callers only. A
	// config-authored step passes the zero Caller and is not gated — the same
	// trusted/untrusted split every other resource check makes.
	if !c.AgentFacing {
		return nil
	}
	if gp := m.scopeGuard.Load(); gp != nil {
		return (*gp)(c, op, strings.TrimSpace(scope))
	}
	return nil
}

// ScopeOf returns the stored scope of one entry, for ops that address a
// memory by id rather than by scope. Ownership on `forget` is exactly this:
// the id's OWN scope goes through CheckOp, so deleting another tenant's
// memory is refused by the same allowlist that governs reading it.
//
// An unknown id reports "" and found=false — the caller returns not-found
// rather than an authorization error, so the error can't be used to probe for
// ids in scopes the caller cannot see.
func (m *Manager) ScopeOf(id string) (scope string, found bool, err error) {
	if strings.TrimSpace(id) == "" {
		return "", false, fmt.Errorf("memory: id is required")
	}
	all, err := m.backend.List()
	if err != nil {
		return "", false, err
	}
	for _, e := range all {
		if e.ID == id {
			return e.Scope, true, nil
		}
	}
	return "", false, nil
}

// callerKey carries the authorization caller for memory ops reached through a
// connector, where the gate has no other channel to the flow layer.
type callerKey struct{}

// WithCaller marks ctx as carrying AGENT-FACING memory ops on behalf of one
// dispatch. The flow layer stamps it at the point it decides the call is
// agent-authored (or a skill tool call); the memory verbs read it back.
//
// The caller is stamped WHOLE — repo included — rather than being read back
// out of the provenance Source. The two answer different questions (who to
// record vs. who to authorize), they are stamped at different places, and an
// authorization that depended on a stamp applied somewhere far away is the
// kind of seam that let this allowlist go inert in the first place.
func WithCaller(ctx context.Context, c Caller) context.Context {
	c.AgentFacing = true
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom reads the authorization caller back. The zero value — a
// config-authored step, which stamps nothing — is not gated, which is
// deliberate and is why the marker is explicit rather than inferred.
func CallerFrom(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}
