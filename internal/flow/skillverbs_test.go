package flow

import (
	"context"
	"strings"
	"testing"
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
