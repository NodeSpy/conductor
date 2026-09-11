package flow

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// MEMORY SCOPE, on every agent-facing face (round-5 #1).
//
// `policy.agent_authored.allow_memory_scopes` was enforced in exactly one
// place: the `run: code` binding's DataGuard. The memory VERBS — reachable
// from `skill.verbs: [mem.*]` and from an agent-authored `uses: memory.*`
// step — went through memory.CheckOp, which layers the unconditional
// reserved-bucket rule over "the operator's per-scope allowlist, installed by
// the flow layer". Nothing ever installed it. `SetScopeGuard` had no caller
// outside a unit test, so the allowlist was inert on that face and a grant
// for one scope wrote, read, and forgot in every other.
//
// The round-3 meta-test missed it because it asserted CheckOp is CALLED, not
// that the allowlist behind it is ENFORCED — a gate wired to nothing still
// answers the phone.
//
// This is the missing installer. It builds the guard from the same policy and
// the SAME function the code face uses (resourcePolicy.memoryScopeOK), so the
// two faces cannot mean different things by one config line, and it is
// installed at boot so the enforcement is on the daemon's real path.
//
// Why memory keeps its own dimension rather than joining the connector-
// declared Field.Scope walk: two of its rules have no expression there. A
// scope resolves relative forms ("repo" → "repo:<owner/repo>"), and `forget`
// addresses an ID whose scope is the STORED entry's — a generic walk over a
// verb's options can see neither. memory.CheckOp already resolves both before
// it authorizes, so the gate belongs there.

// MemoryScopeGuard builds the agent-facing memory scope allowlist from the
// global agent_authored policy. Install it once at boot (memory.SetScopeGuard).
//
// Semantics, matching the code face exactly:
//
//	trust: full          → nil guard: everything allowed, as it lifts every
//	                       other resource allowlist.
//	no policy block      → the allowlist is empty, NOT absent: the caller's
//	                       own scope and nothing else. A skill grant's scoping
//	                       does not wait for a policy block to exist (round-4
//	                       F3), and memory is no exception. An agent-authored
//	                       PLAN cannot run without a policy block at all, so
//	                       this case is really about the skill surface.
//	otherwise            → own scope ∪ allow_memory_scopes, deny by default,
//	                       and an UNSCOPED op refused (a recall naming no
//	                       scope would read every tenant's entries).
//
// The reserved `global` bucket is refused before this runs and no allowlist
// can open it (memory.CheckAgentScope).
func MemoryScopeGuard(cfg *config.Config) memory.ScopeGuard {
	var pol *config.AgentAuthoredPolicy
	if cfg != nil && cfg.Policy != nil {
		pol = cfg.Policy.AgentAuthored
	}
	if pol.TrustFull() {
		return func(memory.Caller, string, string) error { return nil }
	}
	var allow []string
	if pol != nil {
		allow = pol.AllowMemoryScopes
	}
	return func(c memory.Caller, op, scope string) error {
		// The caller's own repo is what makes its own scope implicitly
		// allowed; everything else comes from the operator's list.
		rp := &resourcePolicy{scopes: allow, trigger: c.Repo}
		if rp.memoryScopeOK(scope) {
			return nil
		}
		named := scope
		if named == "" {
			named = "(none named)"
		}
		return fmt.Errorf("agent_authored allowlist: %s memory scope %s — not in policy.agent_authored.allow_memory_scopes, and not this dispatch's own scope (trust: full lifts this)", op, named)
	}
}
