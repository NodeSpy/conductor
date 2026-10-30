package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// buildableForTest forces the "can this box build the sandbox?" probe to a
// fixed answer for the duration of a test, so the seam's enforcement decision
// is deterministic regardless of the host's user-namespace support.
func buildableForTest(t *testing.T, can bool) {
	t.Helper()
	old := agentSandboxBuildable
	agentSandboxBuildable = func() bool { return can }
	t.Cleanup(func() { agentSandboxBuildable = old })
}

// Bots are confined by default: a config with no sandbox stanza resolves to
// sandbox-on, and the seam names the conductor-owned sandboxed provider entry.
func TestSandboxSeamDefaultOn(t *testing.T) {
	buildableForTest(t, true)
	cfg := &config.Config{} // no sandbox stanza — the common existing config
	if !cfg.AgentSandboxOn() {
		t.Fatal("config-compat: a config with no sandbox stanza must resolve to sandbox-on")
	}
	s := sandboxSeam(cfg)
	if !s.Active() {
		t.Fatal("default must be confined: Active() = false for a config with no sandbox stanza")
	}
	prov, ok := s.PaseoProvider("claude")
	if !ok || prov != "claude-sandboxed" {
		t.Fatalf("PaseoProvider(claude) = %q,%v, want claude-sandboxed,true", prov, ok)
	}
	// An empty base is paseo's default provider, sandboxed.
	prov, ok = s.PaseoProvider("")
	if !ok || prov != "claude-sandboxed" {
		t.Fatalf("PaseoProvider(\"\") = %q,%v, want claude-sandboxed,true", prov, ok)
	}
	// An already-sandboxed name is idempotent.
	if prov, ok := s.PaseoProvider("codex-sandboxed"); !ok || prov != "codex-sandboxed" {
		t.Fatalf("PaseoProvider(codex-sandboxed) = %q,%v, want codex-sandboxed,true", prov, ok)
	}
}

// The explicit opt-out turns the sandbox off: Active() is false and the seam
// names no sandboxed provider, so the gate refuses promotion past observe.
func TestSandboxSeamOptOut(t *testing.T) {
	buildableForTest(t, true)
	cfg := &config.Config{Sandbox: &config.SandboxConfig{Agents: "off"}}
	if cfg.AgentSandboxOn() {
		t.Fatal("sandbox.agents: off must resolve to sandbox-off")
	}
	s := sandboxSeam(cfg)
	if s.Active() {
		t.Fatal("opt-out: Active() = true, want false")
	}
	if prov, ok := s.PaseoProvider("claude"); ok {
		t.Fatalf("opt-out named a sandboxed provider %q", prov)
	}
}

// An explicit `on` is confined too (and equals the default).
func TestSandboxSeamExplicitOn(t *testing.T) {
	buildableForTest(t, true)
	cfg := &config.Config{Sandbox: &config.SandboxConfig{Agents: "on"}}
	if !sandboxSeam(cfg).Active() {
		t.Fatal("sandbox.agents: on must be confined")
	}
}

// End to end: the REAL seam drives the gate's promotion precondition. With the
// seam active (default on, buildable) a promoted reversible verb runs for the
// bot's own home session; with the seam off (opt-out) the same verb is refused
// — it becomes a proposal, never an autonomous run. This proves the wiring, not
// just a fixed test jail.
func TestSeamDrivesGatePromotion(t *testing.T) {
	falsep := func() *bool { b := false; return &b }
	bcfg := func() config.BotConfig {
		return config.BotConfig{
			Skill: &config.SkillPolicy{Verbs: []string{"*"}},
			Autonomy: config.BotAutonomy{
				Shadow:  falsep(), // out of shadow
				Tiers:   map[string][]string{config.TierReversible: {"pd.ack"}},
				Promote: []string{"pd.ack"},
			},
		}
	}
	id := flow.SkillIdentity{Bot: "sre", BotRole: config.BotRoleHome, Dispatch: "h1"}

	run := func(t *testing.T, buildable bool, agents string) flow.BotVerdict {
		buildableForTest(t, buildable)
		cfg := &config.Config{Bots: map[string]config.BotConfig{"sre": bcfg()}}
		if agents != "" {
			cfg.Sandbox = &config.SandboxConfig{Agents: agents}
		}
		st, err := store.Open(store.Options{StatePath: filepath.Join(t.TempDir(), "s.json")})
		if err != nil {
			t.Fatal(err)
		}
		gate := &bot.Gate{Cfg: func() *config.Config { return cfg }, Ledger: st, Sandbox: sandboxSeam(cfg)}
		v, err := gate.CheckVerb(context.Background(), id, "pd.ack", nil)
		if err != nil {
			t.Fatalf("CheckVerb: %v", err)
		}
		return v
	}

	// Sandbox on (default) + buildable → the promoted verb runs autonomously.
	if v := run(t, true, ""); !v.Allow {
		t.Fatalf("seam active: promoted reversible verb did not run (verdict %+v)", v)
	}
	// Explicit opt-out → promotion refused: a proposal, not an autonomous run.
	if v := run(t, true, "off"); v.Allow {
		t.Fatal("seam off (opt-out): promoted verb ran autonomously — promotion must be refused")
	}
	// On but the platform cannot sandbox → fail closed: promotion refused.
	if v := run(t, false, ""); v.Allow {
		t.Fatal("platform cannot sandbox: promoted verb ran — must fail closed")
	}
}

// Fail closed: with the sandbox ON but the platform unable to build it, the
// seam is NOT active — the gate refuses autonomous promotion rather than let
// an agent run unconfined.
func TestSandboxSeamFailsClosedWhenUnbuildable(t *testing.T) {
	buildableForTest(t, false)
	cfg := &config.Config{} // default on
	s := sandboxSeam(cfg)
	if s.Active() {
		t.Fatal("platform cannot sandbox yet Active() = true — must fail closed")
	}
	if prov, ok := s.PaseoProvider("claude"); ok {
		t.Fatalf("unbuildable platform named a sandboxed provider %q", prov)
	}
}
