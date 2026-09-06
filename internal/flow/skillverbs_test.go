package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
)

func skillRig(t *testing.T) (*testRig, *fakeState) {
	t.Helper()
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
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

	// Globs expand; empty patterns expose nothing.
	if got := rig.Runner.SkillVerbCatalog([]string{"svc.*"}); len(got) != 4 {
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
  svc: { type: fake }
agents:
  deployer:
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
  svc: { type: fake }
agents:
  deployer:
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
  svc: { type: fake }
agents:
  deployer: { model: x, skill: { verbs: ["svc.*"] } }
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
  svc: { type: fake }
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
