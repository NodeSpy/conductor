package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

func skillRig(t *testing.T) (*testRig, *fakeState) {
	t.Helper()
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	return newTestRunner(t, cfg, reg), st
}

// The catalog lists exactly what the profile's skill.verbs patterns expose,
// shaped as MCP tool declarations.
func TestSkillVerbCatalog(t *testing.T) {
	rig, _ := skillRig(t)
	tools := rig.Runner.SkillVerbCatalog([]string{"svc.post"})
	if len(tools) != 1 {
		t.Fatalf("catalog: %+v", tools)
	}
	tool := tools[0]
	if tool["name"] != "svc_post" || tool["uses"] != "svc.post" {
		t.Fatalf("tool naming: %+v", tool)
	}
	schema := tool["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if props["text"].(map[string]any)["type"] != "string" {
		t.Fatalf("schema: %+v", schema)
	}
	if req := schema["required"].([]string); len(req) != 1 || req[0] != "text" {
		t.Fatalf("required: %+v", schema)
	}

	// Globs expand (post/ask/fail/slow/download/upload); empty patterns
	// expose nothing.
	if got := rig.Runner.SkillVerbCatalog([]string{"svc.*"}); len(got) != 6 {
		t.Fatalf("glob catalog: %d tools", len(got))
	}
	if got := rig.Runner.SkillVerbCatalog(nil); len(got) != 0 {
		t.Fatalf("empty patterns must expose nothing: %+v", got)
	}
}

// A gated call dispatches the REAL verb with the agent's options passed
// through literally — never template-rendered — and audits via: skill.
func TestRunSkillVerbDispatches(t *testing.T) {
	rig, st := skillRig(t)
	id := SkillIdentity{Agent: "deployer", Repo: "o/r", Number: 7, Verbs: []string{"svc.*"}}
	out, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post",
		map[string]any{"text": "hello {{.secrets.tok}}"})
	if err != nil {
		t.Fatal(err)
	}
	if out["id"] == nil {
		t.Fatalf("outputs: %+v", out)
	}
	calls := st.snapshot()
	if len(calls) != 1 || calls[0].Verb != "post" {
		t.Fatalf("calls: %+v", calls)
	}
	// LITERAL pass-through: the template action must NOT have been evaluated.
	if got := calls[0].Opts["text"]; got != "hello {{.secrets.tok}}" {
		t.Fatalf("options must pass through unrendered, got %v", got)
	}
	audits := rig.Store.auditsWithEvent("verb")
	found := false
	for _, e := range audits {
		if e["via"] == "skill" && e["uses"] == "svc.post" && e["outcome"] == "ok" && e["agent"] == "deployer" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no via:skill audit entry: %+v", audits)
	}
}

// Outputs are redacted before crossing back to the agent.
func TestRunSkillVerbRedactsOutputs(t *testing.T) {
	rig, st := skillRig(t)
	rig.Runner.Secrets.Track("s3kr1t-value")
	st.outputs["post"] = map[string]any{"id": 1, "echo": "leak s3kr1t-value here"}
	id := SkillIdentity{Agent: "a", Verbs: []string{"svc.post"}}
	out, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post", map[string]any{"text": "x"})
	if err != nil {
		t.Fatal(err)
	}
	echo, _ := out["echo"].(string)
	if strings.Contains(echo, "s3kr1t-value") {
		t.Fatalf("output leaked a tracked secret: %q", echo)
	}
	if !strings.Contains(echo, "«redacted»") {
		t.Fatalf("output must be redacted: %q", echo)
	}
}

// Deny-by-default gating: outside the patterns, workflow/conductor always,
// and conductor.* even under a wildcard.
func TestRunSkillVerbGating(t *testing.T) {
	rig, st := skillRig(t)
	cases := []struct {
		verbs []string
		uses  string
		want  string
	}{
		{[]string{"svc.post"}, "svc.fail", "skill.verbs"},
		{nil, "svc.post", "skill.verbs"},
		{[]string{"*"}, "conductor.pause", "run_step"},
		{[]string{"*"}, "workflow.run", "run_step"},
		{[]string{"*"}, "nosuch.post", "unknown connector"},
		{[]string{"*"}, "malformed", "malformed"},
	}
	for _, c := range cases {
		_, err := rig.Runner.RunSkillVerb(context.Background(),
			SkillIdentity{Agent: "a", Verbs: c.verbs}, c.uses, map[string]any{"text": "x"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("uses=%s verbs=%v: want %q denial, got %v", c.uses, c.verbs, c.want, err)
		}
	}
	if calls := st.snapshot(); len(calls) != 0 {
		t.Fatalf("denied calls must not dispatch: %+v", calls)
	}
}

// The relay barrier holds on the skill surface: tracked secret material in
// agent-supplied options never reaches an external connector.
func TestRunSkillVerbSecretRelayBarrier(t *testing.T) {
	rig, st := skillRig(t)
	rig.Runner.Secrets.Track("s3kr1t-value")
	id := SkillIdentity{Agent: "a", Verbs: []string{"svc.*"}}
	_, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post",
		map[string]any{"text": "exfil s3kr1t-value"})
	if err == nil || !strings.Contains(err.Error(), "refusing to relay secret material") {
		t.Fatalf("want relay refusal, got %v", err)
	}
	if calls := st.snapshot(); len(calls) != 0 {
		t.Fatalf("refused call must not dispatch: %+v", calls)
	}
	// And the audit trail shows the denial without the value.
	for _, e := range rig.Store.auditsWithEvent("verb") {
		if s, ok := e["error"].(string); ok && strings.Contains(s, "s3kr1t-value") {
			t.Fatalf("audit leaked the value: %+v", e)
		}
	}
}

// #122 R2: the skill surface must not silently exceed the plan surface.
// A verb policy.agent_authored.approve gates behind human approval is
// rejected at CONFIG LOAD when a skill.verbs pattern would admit it, and
// refused at runtime even if such a config slipped through.
func TestSkillVerbsCannotBypassApprove(t *testing.T) {
	// Load-time: skill gh-wildcard vs an approve-listed concrete verb.
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  deployer:
    type: agent
    name: deployer
    model: x
    skill: { verbs: ["svc.*"] }
policy:
  agent_authored:
    allow: [ svc.ask ]
    approve: [ svc.post ]
`)
	err := Validate(cfg, buildRegistry(t, cfg))
	if err == nil || !strings.Contains(err.Error(), `skill.verbs admits "svc.post"`) {
		t.Fatalf("approve-overlap must fail validation, got %v", err)
	}

	// Non-overlapping skill.verbs validate fine.
	ok := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  deployer:
    type: agent
    name: deployer
    model: x
    skill: { verbs: ["svc.ask"] }
policy:
  agent_authored:
    approve: [ svc.post ]
`)
	if err := Validate(ok, buildRegistry(t, ok)); err != nil {
		t.Fatalf("non-overlapping skill.verbs must validate: %v", err)
	}

	// Runtime belt: even with the verb pattern-admitted, the approve gate
	// refuses on the skill surface and nothing dispatches.
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	id := SkillIdentity{Agent: "deployer", Verbs: []string{"svc.*"}}
	_, rerr := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post", map[string]any{"text": "x"})
	if rerr == nil || !strings.Contains(rerr.Error(), "approval") {
		t.Fatalf("approve-gated verb must be refused on the skill surface, got %v", rerr)
	}
	if calls := st.snapshot(); len(calls) != 0 {
		t.Fatalf("refused call must not dispatch: %+v", calls)
	}
	// trust: full lifts approve everywhere — the skill surface follows.
	full := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  deployer: { type: agent, name: deployer, model: x, skill: { verbs: ["svc.*"] } }
policy:
  agent_authored: { trust: full, approve: [ svc.post ] }
`)
	if err := Validate(full, buildRegistry(t, full)); err != nil {
		t.Fatalf("trust: full must lift the overlap check: %v", err)
	}
}

// #122 R2 (egress belt): the internal WRITE barrier holds on the skill
// surface — tracked secret material never lands in shared state through an
// agent tool call.
func TestRunSkillVerbSecretWriteBarrier(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets.Track("s3kr1t-value")
	id := SkillIdentity{Agent: "a", Verbs: []string{"kv.*"}}
	_, err := rig.Runner.RunSkillVerb(context.Background(), id, "kv.set",
		map[string]any{"store": "main", "namespace": "n", "key": "k", "value": "park s3kr1t-value"})
	if err == nil || !strings.Contains(err.Error(), "refusing to write secret material") {
		t.Fatalf("want write-barrier refusal, got %v", err)
	}
	// A clean write still works — the barrier is value-triggered, not a ban.
	if _, err := rig.Runner.RunSkillVerb(context.Background(), id, "kv.set",
		map[string]any{"store": "main", "namespace": "n", "key": "k", "value": "plain"}); err != nil {
		t.Fatalf("clean kv.set: %v", err)
	}
}

// Identity is a per-verb concern: the skill layer never forces an `as:`. A
// verb's own `as:` option travels through exactly as the agent supplied it
// (or absent → the connector's own default, e.g. gh: me). The old #122-R4
// forcing (skill.identity / agent_authored.identity injected onto writes) is
// gone.
func TestSkillVerbIdentity(t *testing.T) {
	// Agent-supplied `as` passes through untouched.
	rig, st := skillRig(t)
	id := SkillIdentity{Agent: "a", Verbs: []string{"svc.*"}}
	if _, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post",
		map[string]any{"text": "x", "as": "bot"}); err != nil {
		t.Fatal(err)
	}
	if calls := st.snapshot(); len(calls) != 1 || calls[0].Opts["as"] != "bot" {
		t.Fatalf("agent-supplied as must pass through unchanged: %+v", calls)
	}

	// No `as` supplied → the skill layer injects nothing; the option stays
	// absent so the connector applies its own default.
	rig2, st2 := skillRig(t)
	if _, err := rig2.Runner.RunSkillVerb(context.Background(), id, "svc.post",
		map[string]any{"text": "x"}); err != nil {
		t.Fatal(err)
	}
	if calls := st2.snapshot(); len(calls) != 1 {
		t.Fatalf("expected one call: %+v", calls)
	} else if v, present := calls[0].Opts["as"]; present {
		t.Fatalf("skill layer must not inject an identity, got as=%v", v)
	}

	// A policy.agent_authored.identity is NOT injected onto skill verbs either
	// (that fallback is gone with the redesign).
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
policy:
  agent_authored: { identity: polbot }
`)
	reg := buildRegistry(t, cfg)
	st3 := newFakeState(t, "svc")
	rig3 := newTestRunner(t, cfg, reg)
	if _, err := rig3.Runner.RunSkillVerb(context.Background(),
		SkillIdentity{Agent: "a", Verbs: []string{"svc.*"}}, "svc.post",
		map[string]any{"text": "x"}); err != nil {
		t.Fatal(err)
	}
	if calls := st3.snapshot(); len(calls) != 1 {
		t.Fatalf("expected one call: %+v", calls)
	} else if v, present := calls[0].Opts["as"]; present {
		t.Fatalf("agent_authored.identity must NOT be injected onto skill verbs, got as=%v", v)
	}
}

// The redesign removes the load-time identity requirement: a skill profile
// whose verbs admit an as-taking write verb needs no skill.identity (nor
// agent_authored.identity) — identity is a per-verb `as:` option defaulting
// to the connector's own default.
func TestValidateSkillNoIdentityNeeded(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  deployer: { type: agent, name: deployer, model: x, skill: { verbs: ["svc.*"] } }
`)
	if err := Validate(cfg, buildRegistry(t, cfg)); err != nil {
		t.Fatalf("write-capable skill profile without identity must now validate: %v", err)
	}
}

// #122 R5a: skill.verbs patterns are checked against the real registry —
// unknown literal connectors and never-served surfaces are load errors;
// a pattern matching nothing is a validate warning.
func TestValidateSkillVerbPatterns(t *testing.T) {
	cases := []struct{ verbs, wantErr string }{
		{`["nosuch.post"]`, `unknown connector "nosuch"`},
		{`["nosuch.*"]`, `unknown connector "nosuch"`},
		{`["svc"]`, "is not a verb"},
		{`["workflow.run"]`, "never served on the skill surface"},
		{`["conductor.pause"]`, "never served on the skill surface"},
	}
	for _, c := range cases {
		cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  a: { type: agent, name: a, model: x, skill: { verbs: `+c.verbs+` } }
`)
		err := Validate(cfg, buildRegistry(t, cfg))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("verbs %s: want %q, got %v", c.verbs, c.wantErr, err)
		}
	}

	// A well-formed pattern that matches nothing warns (not errors).
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
runtimes:
  gem: { agent: gemini, default: true }
steps:
  a: { type: agent, name: a, model: x, skill: { verbs: ["svc.nosuchverb"] } }
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("zero-match pattern must not error: %v", err)
	}
	warns := SkillWarnings(cfg, reg)
	if len(warns) != 1 || !strings.Contains(warns[0], "matches no verb") {
		t.Fatalf("zero-match pattern must warn: %v", warns)
	}
	// Live patterns warn nothing.
	live := loadConfig(t, `
connectors:
  svc: { use: fake }
runtimes:
  gem: { agent: gemini, default: true }
steps:
  a: { type: agent, name: a, model: x, skill: { verbs: ["svc.*"] } }
`)
	regLive := buildRegistry(t, live)
	if warns := SkillWarnings(live, regLive); len(warns) != 0 {
		t.Fatalf("live patterns must not warn: %v", warns)
	}
}

// #123: a skill: profile on a runtime with no MCP launch surface is a
// validate WARNING — the tools and broker cannot reach the agent there.
func TestSkillWarningsUnsupportedRuntime(t *testing.T) {
	// Default (builtin paseo) runtime: local → supported via the CLI face,
	// so no unreachable-endpoint warning.
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  a: { type: agent, name: a, model: x, skill: { verbs: ["svc.ask"] } }
`)
	reg := buildRegistry(t, cfg)
	for _, w := range SkillWarnings(cfg, reg) {
		if strings.Contains(w, "cannot reach the conductor skill surface") {
			t.Fatalf("local paseo default must NOT warn as unreachable: %v", w)
		}
	}

	// Every known runtime is reachable now: local paseo/agent-deck via the CLI
	// face, opencode/acp via MCP, and a remote host: via the SSH reverse tunnel.
	// None warn as unreachable.
	for _, runtime := range []string{
		"pas: { type: paseo, default: true }",
		"deck: { type: agent-deck, default: true }",
		"oc: { type: opencode, default: true }",
		"gem: { agent: gemini, default: true }",
		"rem: { type: paseo, host: build-box, default: true }",
	} {
		y := loadConfig(t, `
connectors:
  svc: { use: fake }
runtimes:
  `+runtime+`
steps:
  a: { type: agent, name: a, model: x, skill: { verbs: ["svc.ask"] } }
`)
		for _, s := range SkillWarnings(y, buildRegistry(t, y)) {
			if strings.Contains(s, "cannot reach the conductor skill surface") {
				t.Fatalf("runtime %q must not warn as unreachable: %v", runtime, s)
			}
		}
	}
}
