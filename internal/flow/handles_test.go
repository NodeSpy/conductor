package flow

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The {{secret "name"}} boundary-handle tests (#36 §12 increment 2): the
// handle is what every rendered surface shows; the real value appears only
// in what conductor itself sends out, and never for agent-authored steps.

const handleCfg = `
connectors:
  svc: { use: fake }
secrets:
  tok: env:FLOW_HANDLE_TEST_TOK
`

// A config-authored verb step: the outbound invocation carries the real
// value; the audit entry keeps the opaque handle.
func TestSecretHandleVerbBoundary(t *testing.T) {
	cfg := loadConfig(t, handleCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: a
    uses: svc.post
    options:
      text: 'x {{secret "tok"}} y'
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets.Track("s3kr1t-value")
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "x s3kr1t-value y" {
		t.Fatalf("outbound options must carry the real value: %+v", calls)
	}
	// Audit shows the handle — not the value, not even its redaction.
	for _, e := range rig.Store.auditsWithEvent("verb") {
		opts, _ := e["options"].(map[string]any)
		text, _ := opts["text"].(string)
		if !strings.Contains(text, secrets.Handle("tok")) {
			t.Fatalf("audit options must keep the handle: %+v", e)
		}
		if strings.Contains(text, "s3kr1t-value") {
			t.Fatalf("audit options leaked the value: %+v", e)
		}
	}
}

// A handle arriving through DATA (a step output an agent could control) is
// not eligible and passes through as inert text — the relay exfiltration
// path stays closed.
func TestSecretHandleRelayedDataStaysOpaque(t *testing.T) {
	cfg := loadConfig(t, handleCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputs["post"] = map[string]any{"id": secrets.Handle("tok")}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: a
    uses: svc.post
    options: { text: seed }
  - id: b
    uses: svc.post
    options: { text: "relay {{.a.id}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 2 {
		t.Fatalf("calls: %+v", calls)
	}
	if got := calls[1].Opts["text"]; got != "relay "+secrets.Handle("tok") {
		t.Fatalf("a data-borne handle must stay opaque, got %v", got)
	}
}

// A config-authored code step: conductor executes the interpreter itself, so
// the env crosses the boundary resolved.
func TestSecretHandleCodeEnvBoundary(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	cfg := loadConfig(t, handleCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: c
    run: sh
    code: printf '%s' "$TOK"
    env:
      TOK: '{{secret "tok"}}'
  - id: rec
    uses: svc.post
    options: { text: "got {{.c.text}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "got s3kr1t-value" {
		t.Fatalf("the code step must see the real value in env: %+v", calls)
	}
}

// Agent-authored plan steps NEVER resolve handles — neither a {{secret}}
// call the agent wrote nor a literal handle it pasted — whatever the trust
// level implies elsewhere.
func TestSecretHandlePlanStepsStayOpaque(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
memory: { type: memory }
secrets:
  tok: env:FLOW_HANDLE_TEST_TOK
steps:
  planner: { type: agent, name: planner, model: x }
policy:
  agent_authored:
    allow: [ svc.post ]
    allow_secrets: ["*"]
`)
	_, fake := dispatchPlanCfg(t, cfg, "```plan\n- id: p\n  uses: svc.post\n  options: { text: 'try {{secret \"tok\"}} and "+secrets.Handle("tok")+"' }\n```")
	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	text, _ := calls[0].Opts["text"].(string)
	if strings.Contains(text, "s3kr1t-value") {
		t.Fatalf("a plan step resolved a secret handle: %q", text)
	}
	if !strings.Contains(text, secrets.Handle("tok")) {
		t.Fatalf("plan step must carry the opaque handle: %q", text)
	}
}

// dispatchPlanCfg is dispatchPlan with an explicit config (the shared helper
// builds its own).
func dispatchPlanCfg(t *testing.T, cfg *config.Config, output string) (*testRig, *fakeState) {
	t.Helper()
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: output}, nil
	}
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), mustSpec(t, planSpec))
	return rig, fake
}

// Saved (agent-promoted) workflows are agent-authored too: reviewed or not,
// their steps keep handles opaque.
func TestSecretHandleSavedWorkflowStaysOpaque(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
secrets:
  tok: env:FLOW_HANDLE_TEST_TOK
policy:
  agent_authored:
    allow: [ svc.post ]
    allow_secrets: ["*"]
`)
	sw, _ := OpenSavedStore("")
	if _, err := sw.Save("relay", "d", []config.Step{{
		ID: "s", Uses: "svc.post",
		Options: map[string]any{"text": `try {{secret "tok"}}`},
	}}, memory.Source{Step: "planner"}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Review("relay"); err != nil {
		t.Fatal(err)
	}
	ConfigureSavedWorkflows(sw)
	t.Cleanup(func() { ConfigureSavedWorkflows(nil) })

	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: w
    workflow: relay
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	text, _ := calls[0].Opts["text"].(string)
	if strings.Contains(text, "s3kr1t-value") {
		t.Fatalf("a saved workflow resolved a secret handle: %q", text)
	}
	if !strings.Contains(text, secrets.Handle("tok")) {
		t.Fatalf("saved workflow must carry the opaque handle: %q", text)
	}
}

// Load-time validation: a literal {{secret "name"}} call must name a
// configured secrets: entry, and the name must be literal.
func TestValidateSecretCallNames(t *testing.T) {
	base := `
connectors:
  svc: { use: fake }
secrets:
  tok: env:FLOW_HANDLE_TEST_TOK
triggers:
  - on: svc.ping
    steps:
      - id: a
        uses: svc.post
        options: { text: '%s' }
`
	good := loadConfig(t, strings.Replace(base, "%s", `{{secret "tok"}}`, 1))
	if err := Validate(good, buildRegistry(t, good)); err != nil {
		t.Fatalf("known secret name must validate: %v", err)
	}
	bad := loadConfig(t, strings.Replace(base, "%s", `{{secret "nope"}}`, 1))
	if err := Validate(bad, buildRegistry(t, bad)); err == nil || !strings.Contains(err.Error(), "no secret named") {
		t.Fatalf("unknown secret name must fail validation: %v", err)
	}
	dyn := loadConfig(t, strings.Replace(base, "%s", `{{secret .msg}}`, 1))
	if err := Validate(dyn, buildRegistry(t, dyn)); err == nil || !strings.Contains(err.Error(), "literal") {
		t.Fatalf("non-literal secret name must fail validation: %v", err)
	}
}

// The current-model naming: {{secret "<vault>/<key>"}} resolves through the
// vaults registry at the boundary; the outbound call carries the value, the
// audit carries the handle, and the taint keeps it out of every log line.
func TestSecretHandleVaultForm(t *testing.T) {
	rig, fake, _ := vaultRig(t, "")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: a
    uses: svc.post
    options:
      text: 'tok={{secret "house/gh"}}'
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "tok=hunter2-tok" {
		t.Fatalf("outbound options must carry the vault value: %+v", calls)
	}
	for _, e := range rig.Store.auditsWithEvent("verb") {
		opts, _ := e["options"].(map[string]any)
		text, _ := opts["text"].(string)
		if !strings.Contains(text, secrets.Handle("house/gh")) {
			t.Fatalf("audit options must keep the handle: %+v", e)
		}
		if strings.Contains(text, "hunter2-tok") {
			t.Fatalf("audit options leaked the vault value: %+v", e)
		}
	}
	// An unknown vault in a handle fails the step loudly.
	spec = mustSpec(t, `
on: svc.ping
steps:
  - id: b
    uses: svc.post
    options: { text: '{{secret "ghost/k"}}' }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "ghost") {
		t.Fatalf("unknown vault must fail the step: %v %q", failed, errStr)
	}
}

// Load-time validation of the vault form: the vault half must exist.
func TestValidateSecretCallVaultNames(t *testing.T) {
	base := `
connectors:
  svc: { use: fake }
vaults:
  house: { type: file, dir: /run/secrets }
triggers:
  - on: svc.ping
    steps:
      - id: a
        uses: svc.post
        options: { text: '%s' }
`
	good := loadConfig(t, strings.Replace(base, "%s", `{{secret "house/gh"}}`, 1))
	if err := Validate(good, buildRegistry(t, good)); err != nil {
		t.Fatalf("known vault must validate: %v", err)
	}
	bad := loadConfig(t, strings.Replace(base, "%s", `{{secret "ghost/gh"}}`, 1))
	if err := Validate(bad, buildRegistry(t, bad)); err == nil || !strings.Contains(err.Error(), "no vault named") {
		t.Fatalf("unknown vault must fail validation: %v", err)
	}
}

// #122 R3 end to end: a config step whose OUTPUT contains a resolved secret
// (e.g. a code step echoing the auth header a {{secret}} handle resolved
// into) must not surface the value in a later agent step's rendered prompt —
// the dispatch scrubber redacts step outputs before the external runtime
// renders against them.
func TestSecretInStepOutputDoesNotReachAgentPrompt(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  fixer: { type: agent, name: fixer, model: x }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputs["post"] = map[string]any{"echo": "Authorization: s3kr1t-value"}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: leaky
    uses: svc.post
    options: { text: seed }
  - id: fix
    type: agent
    extends: fixer
    prompt: "fix using {{.leaky.echo}}"
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets.Track("s3kr1t-value")
	dispatch.SetScrubber(rig.Runner.Secrets)
	t.Cleanup(func() { dispatch.SetScrubber(nil) })

	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	reqs := rig.Agents.requests()
	if len(reqs) != 1 {
		t.Fatalf("agent dispatches: %d", len(reqs))
	}
	// Render exactly what an external runtime would receive.
	prompt, err := dispatch.RenderPrompt(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "s3kr1t-value") {
		t.Fatalf("agent prompt leaked the resolved secret: %q", prompt)
	}
	if !strings.Contains(prompt, secrets.Placeholder) {
		t.Fatalf("agent prompt must carry the placeholder: %q", prompt)
	}
}
