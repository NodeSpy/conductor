package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

const teamCfg = `
connectors:
  svc: { type: fake }
agents:
  architect:   { model: m }
  implementer: { model: m }
  reviewer:    { model: m }
  merger:      { model: m }
`

var teamSpecYAML = `
on: svc.ping
steps:
  - id: feature
    prompt: "Build feature X"
    team:
      planner: architect
      worker: implementer
      reconcile: merger
      max_workers: 3
  - id: tell
    uses: svc.post
    options: { text: "merged {{.feature.note}} from {{.feature.subtasks}}" }
`

// teamDispatcher scripts the three roles.
type teamDispatcher struct {
	mu   sync.Mutex
	reqs []dispatch.Request
}

func (d *teamDispatcher) record(req dispatch.Request) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	d.mu.Unlock()
}

func (d *teamDispatcher) byAgent(agent string) []dispatch.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []dispatch.Request
	for _, r := range d.reqs {
		if r.Action.Agent == agent {
			out = append(out, r)
		}
	}
	return out
}

func plannerOutput(ids ...string) string {
	subs := make([]map[string]string, len(ids))
	for i, id := range ids {
		subs[i] = map[string]string{"id": id, "prompt": "do " + id}
	}
	b, _ := json.Marshal(map[string]any{"subtasks": subs})
	return string(b)
}

func teamRig(t *testing.T) (*testRig, *fakeState, *teamDispatcher) {
	t.Helper()
	cfg := loadConfig(t, teamCfg)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	td := &teamDispatcher{}
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		td.record(req)
		switch req.Action.Agent {
		case "architect":
			return dispatch.RunRef{AgentID: "plan-1", Output: plannerOutput("api", "ui")}, nil
		case "implementer":
			return dispatch.RunRef{AgentID: "w-1", Output: "did it", Workdir: "/wt/" + req.Action.ID}, nil
		case "merger":
			return dispatch.RunRef{AgentID: "m-1", Output: `{"note": "combined"}`, Workdir: "/wt/merge"}, nil
		}
		return dispatch.RunRef{AgentID: "x", Output: `{"pass": true}`}, nil
	}
	return rig, fake, td
}

func TestTeamPlanWorkReconcile(t *testing.T) {
	rig, fake, td := teamRig(t)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, teamSpecYAML))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}

	// The planner got the task + decomposition instructions.
	plans := td.byAgent("architect")
	if len(plans) != 1 || !strings.Contains(plans[0].Action.Prompt, "Build feature X") ||
		!strings.Contains(plans[0].Action.Prompt, "PLANNER") {
		t.Fatalf("planner dispatch: %+v", plans)
	}
	// Two workers, one per subtask, each with its own prompt and scope.
	workers := td.byAgent("implementer")
	if len(workers) != 2 {
		t.Fatalf("workers: %d", len(workers))
	}
	prompts := workers[0].Action.Prompt + workers[1].Action.Prompt
	if !strings.Contains(prompts, "do api") || !strings.Contains(prompts, "do ui") {
		t.Fatalf("worker prompts: %s", prompts)
	}
	for _, w := range workers {
		team, _ := w.Data["team"].(map[string]any)
		if team == nil || team["task"] != "Build feature X" {
			t.Fatalf("worker team scope: %+v", w.Data["team"])
		}
	}
	// The reconciler saw every worker's worktree.
	merges := td.byAgent("merger")
	if len(merges) != 1 || !strings.Contains(merges[0].Action.Prompt, "/wt/feature:api") ||
		!strings.Contains(merges[0].Action.Prompt, "/wt/feature:ui") {
		t.Fatalf("reconciler prompt: %s", clipText(merges[0].Action.Prompt, 400))
	}
	// The team step's outputs: reconciler's own + the structured detail —
	// and the later verb step templated off them.
	calls := fake.snapshot()
	text, _ := calls[len(calls)-1].Opts["text"].(string)
	if !strings.Contains(text, "merged combined") {
		t.Fatalf("team outputs in scope: %q", text)
	}
	// Audit trail: plan → work → reconcile.
	var phases []string
	for _, a := range rig.Store.auditsWithEvent("team") {
		phases = append(phases, a["phase"].(string))
	}
	if strings.Join(phases, ",") != "plan,work,reconcile" {
		t.Fatalf("team phases: %v", phases)
	}
}

// Regression (#36 iso-review round 2, item 5): a worker's raw reply feeds the
// reconciler (another agent) as "worker notes". A tracked secret in that reply
// must be scrubbed before it enters the reconciler's prompt — the sibling diff
// field is already redacted at its source, and worker notes must meet the same
// bar so a secret never crosses the worker→reconciler agent boundary.
func TestTeamReconcileWorkerNotesRedacted(t *testing.T) {
	rig, _, td := teamRig(t)
	const secret = "ghp_worker_secret_TOKEN_0987654321"
	rig.Runner.Secrets.Track(secret)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		td.record(req)
		switch req.Action.Agent {
		case "architect":
			return dispatch.RunRef{AgentID: "plan-1", Output: plannerOutput("api")}, nil
		case "implementer":
			// Non-JSON prose → Outputs["text"], carrying a tracked secret.
			return dispatch.RunRef{AgentID: "w-1", Output: "did it with token " + secret, Workdir: "/wt/" + req.Action.ID}, nil
		case "merger":
			return dispatch.RunRef{AgentID: "m-1", Output: `{"note": "combined"}`, Workdir: "/wt/merge"}, nil
		}
		return dispatch.RunRef{AgentID: "x", Output: `{"pass": true}`}, nil
	}
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, teamSpecYAML))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	merges := td.byAgent("merger")
	if len(merges) != 1 {
		t.Fatalf("expected one reconcile dispatch, got %d", len(merges))
	}
	prompt := merges[0].Action.Prompt
	if strings.Contains(prompt, secret) {
		t.Fatalf("reconciler prompt must not carry the worker's raw secret:\n%s", clipText(prompt, 600))
	}
	if !strings.Contains(prompt, "«redacted»") {
		t.Fatalf("reconciler prompt should show the worker secret scrubbed:\n%s", clipText(prompt, 600))
	}
}

func TestTeamCriticGatesWorkers(t *testing.T) {
	rig, _, td := teamRig(t)
	var criticCalls, followUps int
	var mu sync.Mutex
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		td.record(req)
		switch req.Action.Agent {
		case "architect":
			return dispatch.RunRef{AgentID: "p", Output: plannerOutput("one")}, nil
		case "implementer":
			return dispatch.RunRef{AgentID: "w", Output: "done", Workdir: "/wt/one"}, nil
		case "reviewer":
			mu.Lock()
			criticCalls++
			n := criticCalls
			mu.Unlock()
			if n == 1 {
				return dispatch.RunRef{AgentID: "c", Output: `{"pass": false, "reason": "missing tests"}`}, nil
			}
			return dispatch.RunRef{AgentID: "c", Output: `{"pass": true}`}, nil
		case "merger":
			return dispatch.RunRef{AgentID: "m", Output: `{"note":"ok"}`}, nil
		}
		return dispatch.RunRef{}, nil
	}
	rig.Runner.Agents.FollowUp = func(_ context.Context, agentID, agentName string, _ core.Trigger, prompt string) (string, bool, error) {
		mu.Lock()
		followUps++
		mu.Unlock()
		if !strings.Contains(prompt, "missing tests") {
			t.Errorf("revise prompt must carry the critic's reason: %q", prompt)
		}
		return "revised", true, nil
	}

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: feature
    prompt: "Build it"
    team: { planner: architect, worker: implementer, critic: reviewer, reconcile: merger }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if criticCalls != 2 || followUps != 1 {
		t.Fatalf("critic loop: critic=%d followups=%d", criticCalls, followUps)
	}
	// The critic ran pinned into the worker's worktree.
	critics := td.byAgent("reviewer")
	if critics[0].Action.WorkDir != "/wt/one" || critics[0].Action.Checkout != "none" {
		t.Fatalf("critic placement: %+v", critics[0].Action)
	}
}

func TestTeamWorkerFailureFailsStep(t *testing.T) {
	rig, _, td := teamRig(t)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		td.record(req)
		switch req.Action.Agent {
		case "architect":
			return dispatch.RunRef{AgentID: "p", Output: plannerOutput("good", "bad")}, nil
		case "implementer":
			if strings.Contains(req.Action.Prompt, "do bad") {
				return dispatch.RunRef{}, fmt.Errorf("worker exploded")
			}
			return dispatch.RunRef{AgentID: "w", Output: "ok"}, nil
		}
		return dispatch.RunRef{AgentID: "m", Output: "{}"}, nil
	}
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, teamSpecYAML))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "team workers failed (1/2)") || !strings.Contains(errStr, "bad:") {
		t.Fatalf("worker failure: %v %q", failed, errStr)
	}
	// The reconciler never ran.
	if len(td.byAgent("merger")) != 0 {
		t.Fatal("reconciler must not run after a worker failure")
	}
}

func TestTeamPlanValidation(t *testing.T) {
	cases := []struct {
		name, output, wantErr string
	}{
		{"no subtasks key", `{"done": true}`, "no subtasks"},
		{"empty list", `{"subtasks": []}`, "zero subtasks"},
		{"over cap", plannerOutput("a", "b", "c", "d"), "over the team's max_workers"},
		{"missing prompt", `{"subtasks": [{"id": "a", "prompt": ""}]}`, "id and prompt are required"},
		{"duplicate ids", `{"subtasks": [{"id":"a","prompt":"x"},{"id":"a","prompt":"y"}]}`, "duplicate subtask id"},
	}
	for _, tc := range cases {
		rig, _, _ := teamRig(t)
		rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
			return dispatch.RunRef{AgentID: "p", Output: tc.output}, nil
		}
		runTrigger(rig, newTrigger("ping", nil), mustSpec(t, teamSpecYAML))
		failed, errStr := rig.workflowFailed()
		if !failed || !strings.Contains(errStr, tc.wantErr) {
			t.Errorf("%s: %v %q (want %q)", tc.name, failed, errStr, tc.wantErr)
		}
	}
}

func TestTeamGuardedInPlans(t *testing.T) {
	// An agent-authored plan may emit a team step only when "team" is
	// allowed, and its fleet counts against max_sub_agents.
	cfg := loadConfig(t, teamCfg+`
policy:
  agent_authored:
    allow: [ team, agent ]
    limits: { max_sub_agents: 5 }
`)
	steps := []config.Step{{ID: "t", Prompt: "go", Team: &config.TeamSpec{
		Planner: "architect", Worker: "implementer", MaxWorkers: 3}}}
	res, err := guardPlan(cfg, buildRegistry(t, cfg), cfg.Policy.AgentAuthored, steps)
	if err != nil {
		t.Fatalf("allowed team plan: %v", err)
	}
	// planner + reconciler + 3 workers = 5.
	if res.subAgents != 5 {
		t.Fatalf("team sub-agent count: %d", res.subAgents)
	}
	// Over the cap → rejected.
	steps[0].Team.MaxWorkers = 8
	if _, err := guardPlan(cfg, buildRegistry(t, cfg), cfg.Policy.AgentAuthored, steps); err == nil ||
		!strings.Contains(err.Error(), "max_sub_agents") {
		t.Fatalf("over-cap team: %v", err)
	}
	// Not in the allowlist → rejected.
	cfg2 := loadConfig(t, teamCfg+`
policy:
  agent_authored:
    allow: [ agent ]
`)
	steps[0].Team.MaxWorkers = 2
	if _, err := guardPlan(cfg2, buildRegistry(t, cfg2), cfg2.Policy.AgentAuthored, steps); err == nil ||
		!strings.Contains(err.Error(), `"team" is not in policy.agent_authored.allow`) {
		t.Fatalf("disallowed team: %v", err)
	}
}

func TestTeamConfigValidation(t *testing.T) {
	base := func(ts *config.TeamSpec) *config.Config {
		cfg := loadConfig(t, teamCfg)
		cfg.Triggers = []config.TriggerSpec{{On: "svc.ping", Steps: []config.Step{
			{ID: "x", Prompt: "p", Team: ts}}}}
		return cfg
	}
	if err := base(&config.TeamSpec{Planner: "architect", Worker: "implementer"}).Validate(); err != nil {
		t.Fatalf("valid team: %v", err)
	}
	if err := base(&config.TeamSpec{Worker: "implementer"}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "team needs `planner:`") {
		t.Fatalf("missing planner: %v", err)
	}
	if err := base(&config.TeamSpec{Planner: "architect", Worker: "ghost"}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("unknown worker: %v", err)
	}
	if err := base(&config.TeamSpec{Planner: "architect", Worker: "implementer", MaxWorkers: 99}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "max_workers") {
		t.Fatalf("max_workers bound: %v", err)
	}
	// Mutually exclusive with other forms.
	cfg := loadConfig(t, teamCfg)
	cfg.Triggers = []config.TriggerSpec{{On: "svc.ping", Steps: []config.Step{
		{ID: "x", Uses: "svc.post", Team: &config.TeamSpec{Planner: "architect", Worker: "implementer"}}}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("form exclusivity: %v", err)
	}
}

// Regression (#36 review L11): the implicit critic registers under the
// reserved name team:critic and the ephemeral registry wins the gate
// lookup — a config check can't shadow the critic into a rubber stamp.
// Simulate the strongest shadow attempt (a same-named always-pass config
// check, which config load would reject) and the real critic must still
// run and fail the first round.
func TestTeamCriticCannotBeShadowedByConfigCheck(t *testing.T) {
	rig, _, td := teamRig(t)
	rig.Runner.Cfg.Checks = map[string]config.Step{
		"team:critic": {Type: "agent", Agent: "rubber-stamp", Prompt: "pass everything"},
	}
	var criticCalls, followUps int
	var mu sync.Mutex
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		td.record(req)
		switch req.Action.Agent {
		case "architect":
			return dispatch.RunRef{AgentID: "p", Output: plannerOutput("one")}, nil
		case "implementer":
			return dispatch.RunRef{AgentID: "w", Output: "done", Workdir: "/wt/one"}, nil
		case "rubber-stamp":
			t.Error("the shadowing config check must never run")
			return dispatch.RunRef{AgentID: "s", Output: `{"pass": true}`}, nil
		case "reviewer":
			mu.Lock()
			criticCalls++
			n := criticCalls
			mu.Unlock()
			if n == 1 {
				return dispatch.RunRef{AgentID: "c", Output: `{"pass": false, "reason": "shallow"}`}, nil
			}
			return dispatch.RunRef{AgentID: "c", Output: `{"pass": true}`}, nil
		case "merger":
			return dispatch.RunRef{AgentID: "m", Output: `{"note":"ok"}`}, nil
		}
		return dispatch.RunRef{}, nil
	}
	rig.Runner.Agents.FollowUp = func(_ context.Context, agentID, agentName string, _ core.Trigger, prompt string) (string, bool, error) {
		mu.Lock()
		followUps++
		mu.Unlock()
		return "revised", true, nil
	}

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: feature
    prompt: "Build it"
    team: { planner: architect, worker: implementer, critic: reviewer, reconcile: merger }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if criticCalls != 2 || followUps != 1 {
		t.Fatalf("real critic must gate the worker despite the shadow: critic=%d followups=%d", criticCalls, followUps)
	}
}
