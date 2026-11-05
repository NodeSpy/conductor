package flow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// runContinued runs an agent step `fix` that commits and whose end-of-step
// push lands nothing (no_progress), with the given continue_on_error, then
// `use`, which reads fix's workspace and push report.
func runContinued(t *testing.T, cont string) (*testRig, *fakeState) {
	t.Helper()
	cfg := loadConfig(t, "connectors: { svc: { use: fake } }\n")
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	ws := cloneWithCommits(t, 2)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: ws, Output: "done", AgentID: "a1"}, nil
	}
	rig.Runner.PushWork = func(context.Context, string) (map[string]any, error) { return nil, errNoBranch }
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    prompt: p
    continue_on_error: `+cont+`
  - id: use
    uses: svc.post
    options: { text: "USE failed={{.fix.failed}} n={{.fix.push.unpushed_commits}} in={{if .fix.workdir}}ws{{end}}" }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	return rig, st
}

// continue_on_error as a condition: the run goes on past the failure only
// when it holds — over the failed step's own outputs under its id, and the
// failure — and the step it continued past keeps what it produced.
func TestContinueOnErrorCondition(t *testing.T) {
	for _, tc := range []struct {
		cond      string
		continues bool
	}{
		{`"fix.push.unpushed_commits > 0"`, true},
		{`"fix.push.unpushed_commits > 2"`, false},
		{`"failure.step == \"fix\" && fix.workdir"`, true},
		{`"failure.kind == \"ordinary\""`, false}, // it is no_progress
		{`true`, true},
		{`false`, false},
	} {
		t.Run(tc.cond, func(t *testing.T) {
			rig, st := runContinued(t, tc.cond)
			failed, e := rig.workflowFailed()
			if failed == tc.continues {
				t.Fatalf("failed=%v (%s), want continues=%v", failed, e, tc.continues)
			}
			if !tc.continues {
				if hasPost(st, "USE") {
					t.Fatal("a step after the failure ran")
				}
				return
			}
			if !hasPost(st, "USE failed=true n=2 in=ws") {
				t.Fatalf("the continued step's outputs are kept: %v", postTexts(st))
			}
		})
	}
}

// A failed step keeps its agent (its workspace holds the unpushed commits)
// — unless the run went on past it for later steps to read that workspace:
// then the agent goes when the run ends.
func TestContinuedStepReleasesItsAgentAtRunEnd(t *testing.T) {
	rig, _ := runContinued(t, `"fix.push.unpushed_commits > 0"`)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	if !slices.Equal(rig.Agents.archived, []string{"a1"}) {
		t.Fatalf("archived %v, want the continued step's a1 at run end", rig.Agents.archived)
	}
	rig, _ = runContinued(t, `false`)
	if len(rig.Agents.archived) != 0 {
		t.Fatalf("a failed step's agent was released: %v", rig.Agents.archived)
	}
}

// An agent a later step reads lives for that step and is released when the
// run ends — also when the run fails after it.
func TestLaterReadAgentReleasedAtRunEnd(t *testing.T) {
	for _, tc := range []struct {
		name, mid string
		fails     bool
	}{
		{"done", "", false},
		{"failed", "\n  - { id: boom, uses: svc.fail }", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
			rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
			l := newLiveAgents(t, rig)
			dispatchLive := rig.Agents.dispatchFunc
			var liveAtCheck []string
			rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
				if strings.Contains(req.Action.Prompt, "look in") {
					liveAtCheck, _ = l.state(t)
				}
				return dispatchLive(ctx, req)
			}
			runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, prompt: "fix it" }`+tc.mid+`
  - { id: check, type: agent, prompt: "look in {{.fix.workdir}}" }
`))
			if failed, e := rig.workflowFailed(); failed != tc.fails {
				t.Fatalf("failed=%v (%s)", failed, e)
			}
			if !tc.fails && !slices.Equal(liveAtCheck, []string{"ag-1"}) {
				t.Fatalf("fix's agent is live when check reads it: %v", liveAtCheck)
			}
			if sessions, wts := l.state(t); len(sessions) != 0 || wts != 0 {
				t.Fatalf("after the run: live %v, worktrees %d — want none", sessions, wts)
			}
		})
	}
}

// A continue_on_error condition is checked at load like an if:, over the
// step's scope plus its own outputs and the failure.
func TestValidateContinueOnError(t *testing.T) {
	for _, tc := range []struct{ name, cond, wantErr string }{
		{"its own outputs and the failure", `"fix.push.unpushed_commits > 0 && failure.kind == 'no_progress'"`, ""},
		{"an earlier step", `"meta.title"`, ""},
		{"a malformed condition", `"{{.fix.push"`, "continue_on_error"},
		{"its own outputs, templated", `"{{.fix.push.unpushed_commits}} > 0"`, ""},
		{"true", `true`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, `
connectors: { svc: { use: fake } }
workflows:
  w:
    steps:
      - { id: meta, set: { title: t } }
      - { id: fix, type: agent, prompt: p, continue_on_error: `+tc.cond+` }
      - { id: after, uses: svc.post, options: { text: x } }
`)
			err := Validate(cfg, buildRegistry(t, cfg))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
