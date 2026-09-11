package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// META-TEST (round-5 #1). `policy.agent_authored.allow_memory_scopes` was
// enforced on the `run: code` face and nowhere else. The memory VERBS —
// `skill.verbs: [mem.*]` and an agent-authored `uses: memory.*` — went
// through memory.CheckOp, whose allowlist layer reads a guard that NOTHING IN
// PRODUCTION EVER INSTALLED: `SetScopeGuard` had no caller outside a unit
// test, so the guard was nil, CheckOp fell through to `return nil`, and a
// grant for one scope wrote and read every other.
//
// The round-3 meta-test asserted CheckOp is CALLED. That is not the same
// property as the allowlist being ENFORCED — a gate wired to nothing still
// answers the phone. This one plants a scope the operator did not list and
// asserts every op is REFUSED on both real surfaces, with the daemon's own
// installer in place rather than a guard the test invented.
func TestMemoryScopeAllowlistIsEnforcedOnEveryAgentFacingSurface(t *testing.T) {
	const cfgYAML = `
connectors:
  svc: { use: fake }
memory:
  type: memory
policy:
  agent_authored:
    allow: ["**"]
    allow_memory_scopes: ["repo:only/this-one"]
`
	cfg := loadConfig(t, cfgYAML)
	// The production installer, not a test seam: exactly what the daemon runs
	// when it configures memory.
	mgr := installTestMemory(t, cfg)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner

	// A memory the victim scope already holds, so `forget` has a real id to
	// aim at and the refusal is about the SCOPE, not about a missing entry.
	victim, err := mgr.Remember("victim's note", nil, "repo:victim/other", memory.Source{})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := mgr.Remember("my note", nil, "repo:trigger/repo", memory.Source{})
	if err != nil {
		t.Fatal(err)
	}

	// Every agent-reachable op, aimed at a scope the operator did not list.
	refused := []struct {
		op   string
		opts map[string]any
	}{
		{"remember", map[string]any{"text": "exfiltrate", "scope": "repo:victim/other"}},
		{"recall", map[string]any{"scope": "repo:victim/other"}},
		{"list", map[string]any{"scope": "repo:victim/other"}},
		{"forget", map[string]any{"id": victim.ID}},
		// An UNSCOPED read is the same reach by omission: it would return
		// every scope on the daemon.
		{"recall", map[string]any{}},
		{"list", map[string]any{}},
	}

	t.Run("skill surface", func(t *testing.T) {
		id := SkillIdentity{Agent: "probe", Repo: "trigger/repo", Verbs: []string{"memory.*"}}
		for _, tc := range refused {
			_, err := r.RunSkillVerb(context.Background(), id, "memory."+tc.op, tc.opts)
			if !memoryScopeRefusal(err) {
				t.Errorf("memory.%s %v was NOT refused — a skill grant reached a scope "+
					"allow_memory_scopes does not list (err=%v)", tc.op, tc.opts, err)
			}
		}
		// The dispatch's own scope needs no grant, and the listed scope works.
		for _, ok := range []map[string]any{
			{"text": "mine", "scope": "repo:trigger/repo"},
			{"text": "listed", "scope": "repo:only/this-one"},
		} {
			if _, err := r.RunSkillVerb(context.Background(), id, "memory.remember", ok); err != nil {
				t.Errorf("memory.remember %v must be allowed: %v", ok, err)
			}
		}
		if _, err := r.RunSkillVerb(context.Background(), id, "memory.forget",
			map[string]any{"id": mine.ID}); err != nil {
			t.Errorf("forgetting an entry in the dispatch's OWN scope must be allowed: %v", err)
		}
	})

	t.Run("plan uses: surface", func(t *testing.T) {
		for _, tc := range refused {
			err := r.runAgentAuthoredVerb(t, "memory."+tc.op, tc.opts)
			if !memoryScopeRefusal(err) {
				t.Errorf("an agent-authored `uses: memory.%s` %v was NOT refused (err=%v)",
					tc.op, tc.opts, err)
			}
		}
		if err := r.runAgentAuthoredVerb(t, "memory.remember",
			map[string]any{"text": "mine", "scope": "repo:trigger/repo"}); err != nil {
			t.Errorf("the dispatch's own scope must need no grant: %v", err)
		}
	})

	// CONFIG-AUTHORED steps are untouched: the operator wrote them with their
	// own credential, exactly as with every other resource allowlist.
	t.Run("config-authored uses: is not gated", func(t *testing.T) {
		if _, err := r.execMemoryVerbAsOperator(t, "recall", map[string]any{"scope": "repo:victim/other"}); err != nil {
			t.Errorf("a config-authored memory step must not be scope-gated: %v", err)
		}
	})
}

// trust: full lifts it, like every other resource allowlist.
func TestMemoryScopeGuardTrustFull(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
memory:
  type: memory
policy:
  agent_authored:
    trust: full
`)
	installTestMemory(t, cfg)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	id := SkillIdentity{Agent: "probe", Repo: "trigger/repo", Verbs: []string{"memory.*"}}
	if _, err := r.RunSkillVerb(context.Background(), id, "memory.remember",
		map[string]any{"text": "anywhere", "scope": "repo:any/where"}); err != nil {
		t.Fatalf("trust: full must lift the memory scope allowlist: %v", err)
	}
}

// With NO policy block the allowlist is EMPTY, not ABSENT: the skill surface's
// scoping does not wait for a policy that governs a different surface
// (round-4 F3), so an agent reaches its own scope and nothing else.
func TestMemoryScopeWithoutAPolicyBlock(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
memory:
  type: memory
`)
	installTestMemory(t, cfg)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	id := SkillIdentity{Agent: "probe", Repo: "trigger/repo", Verbs: []string{"memory.*"}}
	if _, err := r.RunSkillVerb(context.Background(), id, "memory.remember",
		map[string]any{"text": "mine", "scope": "repo:trigger/repo"}); err != nil {
		t.Fatalf("the dispatch's own scope must work with no policy block: %v", err)
	}
	_, err := r.RunSkillVerb(context.Background(), id, "memory.remember",
		map[string]any{"text": "theirs", "scope": "repo:victim/other"})
	if !memoryScopeRefusal(err) {
		t.Fatalf("another scope must be refused even with no policy block, got %v", err)
	}
}

// THE CLASS. The guard is only real if something installs it: the round-3
// meta-test proved CheckOp consults a guard, and the bug was that production
// never gave it one. This asserts the daemon's own memory configuration wires
// MemoryScopeGuard — the seam where the whole mechanism went inert.
func TestProductionInstallsTheMemoryScopeGuard(t *testing.T) {
	src := readRepoSource(t, "cmd/conductor/connectors.go")
	if !strings.Contains(src, "SetScopeGuard(flow.MemoryScopeGuard(") {
		t.Error("configureMemory no longer installs flow.MemoryScopeGuard — without it " +
			"memory.CheckOp's allowlist layer is nil and allow_memory_scopes is INERT on " +
			"every agent-facing surface, which is exactly how this shipped once")
	}
}

// memoryScopeRefusal reports a refusal by the memory scope allowlist
// specifically, not by something downstream.
func memoryScopeRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "allow_memory_scopes")
}

// installTestMemory builds a real memory manager for cfg and installs the
// PRODUCTION scope guard on it, mirroring cmd/conductor's configureMemory.
func installTestMemory(t *testing.T, cfg *config.Config) *memory.Manager {
	t.Helper()
	mgr, err := memory.Build(memory.Options{Type: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetScopeGuard(MemoryScopeGuard(cfg))
	memory.Configure(mgr)
	t.Cleanup(func() { memory.Configure(nil) })
	return mgr
}

// runAgentAuthoredVerb drives the PLAN surface the way an agent-authored
// plan step reaches a verb: execVerb, on a context marked agent-authored.
func (r *Runner) runAgentAuthoredVerb(t *testing.T, uses string, opts map[string]any) error {
	t.Helper()
	ctx := markAgentAuthored(context.Background())
	trig := newTrigger("ping", nil)
	trig.Target.Repo = "trigger/repo"
	_, err := r.execVerb(ctx, trig, config.Step{Uses: uses, Options: opts}, "s", map[string]any{}, false)
	return err
}

// execMemoryVerbAsOperator drives the same verb from a CONFIG-AUTHORED step —
// no agent-authored marker — which must not be scope-gated.
func (r *Runner) execMemoryVerbAsOperator(t *testing.T, verb string, opts map[string]any) (map[string]any, error) {
	t.Helper()
	trig := newTrigger("ping", nil)
	trig.Target.Repo = "trigger/repo"
	return r.execVerb(context.Background(), trig, config.Step{Uses: "memory." + verb, Options: opts}, "s", map[string]any{}, false)
}

// readRepoSource reads a file relative to the repository root (this package's
// tests run in internal/flow).
func readRepoSource(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}
