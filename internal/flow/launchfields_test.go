package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// TestDetachStepRequestShape pins what a `detach: true` step hands dispatch:
// the launch fields rendered by the flow template engine (a list-valued
// images: reference flattened), the resolved model, the bare prompt with no
// guidance appended, and the agent_id/workspace_id/branch/path outputs a
// later step reads.
func TestDetachStepRequestShape(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Agents.Guidance = func(string, config.Step, config.Policy) string { return "\nGUIDANCE-MARKER" }
	rig.Runner.Agents.ResolveModel = func(context.Context, config.Step) (string, string, string) { return "m1", "", "prov1" }
	var got dispatch.Request
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req
		return dispatch.RunRef{AgentID: "ag1", WorkspaceID: "ws1", Branch: "handover/x-1", Workdir: "/wt/x", Detached: true}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: t
steps:
  - id: dl
    uses: svc.post
    options: { text: "fetch" }
  - id: launch
    type: agent
    detach: true
    repo: "{{.form.repo}}"
    mode: "{{.form.mode | default \"plan\"}}"
    images: ["{{.shots}}", "/tmp/extra.png"]
    prompt: "look at this"
  - id: tell
    uses: svc.post
    options: { text: "{{.launch.agent_id}} {{.launch.workspace_id}} {{.launch.branch}} {{.launch.path}}" }
`)
	trig := newTrigger("ping", map[string]any{
		"form":  map[string]any{"repo": "acme/widgets"},
		"shots": []any{"/s/a.png", "/s/b.png"},
	})
	runTrigger(rig, trig, spec)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", e)
	}
	if !got.Step.Detach || got.Step.Repo != "acme/widgets" || got.Step.Mode != "plan" {
		t.Fatalf("launch fields not rendered: repo=%q mode=%q", got.Step.Repo, got.Step.Mode)
	}
	if strings.Join(got.Step.Images, ",") != "/s/a.png,/s/b.png,/tmp/extra.png" {
		t.Fatalf("images = %v", got.Step.Images)
	}
	if got.Model != "m1" || got.Provider != "prov1" {
		t.Fatalf("detach must carry the resolved model/provider, got %q/%q", got.Model, got.Provider)
	}
	if got.Action.Prompt != "look at this" {
		t.Fatalf("detach prompt must be exactly the templated prompt, got %q", got.Action.Prompt)
	}
	calls := st.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 verb calls, got %d", len(calls))
	}
	if text, _ := calls[1].Opts["text"].(string); text != "ag1 ws1 handover/x-1 /wt/x" {
		t.Fatalf("detach outputs: %q", text)
	}
}

// TestLaunchFieldsRenderedOnce: a value that arrives through the event (a
// form field) and itself contains template syntax reaches dispatch as
// literal text — never evaluated by a second render.
func TestLaunchFieldsRenderedOnce(t *testing.T) {
	step := config.Step{Repo: "{{.form.repo}}", Branch: "{{.form.branch}}"}
	data := map[string]any{"form": map[string]any{"repo": "a/b", "branch": "x-{{.gh_token}}"}, "gh_token": "SECRET"}
	if err := renderLaunchFields(&step, data); err != nil {
		t.Fatal(err)
	}
	if step.Branch != "x-{{.gh_token}}" {
		t.Fatalf("branch = %q", step.Branch)
	}
}

// TestImagesAcceptStringAndListRefs: an images: item referencing a single
// path string attaches that path; one referencing a list attaches each.
func TestImagesAcceptStringAndListRefs(t *testing.T) {
	step := config.Step{Images: []string{"{{.one}}", "{{.many}}", "{{.missing}}", "/lit.png"}}
	data := map[string]any{"one": "/a.png", "many": []any{"/b.png", "/c.png"}}
	if err := renderLaunchFields(&step, data); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(step.Images, ","); got != "/a.png,/b.png,/c.png,/lit.png" {
		t.Fatalf("images = %s", got)
	}
}

// TestDetachRefusedWhenAgentAuthored: a detach: merged into an agent-authored
// step after plan admission is refused at execution, never launched.
func TestDetachRefusedWhenAgentAuthored(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	called := false
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		called = true
		return dispatch.RunRef{}, nil
	}
	ctx := context.WithValue(context.Background(), agentAuthoredKey{}, true)
	_, _, err := rig.Runner.execAgent(ctx, newTrigger("ping", nil), config.Step{Type: "agent", Detach: true, Repo: "a/b", Prompt: "x"}, "s", "s", map[string]any{}, false)
	if err == nil || called {
		t.Fatalf("agent-authored detach must be refused before dispatch (err=%v called=%v)", err, called)
	}
}

// TestStepRepoOverridesForcedNoCheckout: a non-detach agent step with its own
// `repo:` under a synthetic-target trigger (whose action was forced to
// checkout: none) does NOT inherit the forced "none" — the step named a real
// checkout — and the rendered repo reaches dispatch.
func TestStepRepoOverridesForcedNoCheckout(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	var got dispatch.Request
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: t
steps:
  - { id: s, type: agent, repo: "{{.which}}", prompt: "work" }
`)
	trig := core.Trigger{
		Source: "slack", Instance: "fake", Kind: "ping", TargetTrusted: true,
		Target:  core.Target{Repo: "slack:C1", Number: 9},
		Context: map[string]any{"which": "acme/api"},
		Action:  config.Action{Checkout: "none"},
	}
	runTrigger(rig, trig, spec)
	if got.Step.Repo != "acme/api" {
		t.Fatalf("repo = %q", got.Step.Repo)
	}
	if got.Action.Checkout != "" {
		t.Fatalf("a step with repo: must not inherit the trigger's forced checkout, got %q", got.Action.Checkout)
	}
}
