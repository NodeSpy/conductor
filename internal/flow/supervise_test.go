package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// reviseRig wires a plan-emitting agent plus a scripted Revise service.
type reviseRig struct {
	*testRig
	fake    *fakeState
	prompts []string // revise prompts, in order
}

func newReviseRig(t *testing.T, policy, planOut string, revisions []string) *reviseRig {
	t.Helper()
	cfg := planCfg(t, policy)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: planOut}, nil
	}
	rr := &reviseRig{testRig: rig, fake: fake}
	rig.Runner.Agents.Revise = func(ctx context.Context, agentName string, tr core.Trigger, prompt string) (string, bool, error) {
		rr.prompts = append(rr.prompts, prompt)
		if len(rr.prompts) > len(revisions) {
			return "no plan here, giving up", true, nil
		}
		return revisions[len(rr.prompts)-1], true, nil
	}
	return rr
}

// TestSuperviseReviseSpliceResume: a failing step routes structured context
// to the authoring session; the revision replaces the failed step and
// everything after; committed side-effecting steps NEVER re-run.
func TestSuperviseReviseSpliceResume(t *testing.T) {
	plan := "```plan\n" +
		"- id: first\n  uses: svc.post\n  options: { text: first }\n" +
		"- id: boom\n  uses: svc.fail\n  options: {}\n" +
		"- id: never\n  uses: svc.post\n  options: { text: never }\n" +
		"```"
	revision := "sorry — fixed:\n```plan\n- id: fixed\n  uses: svc.post\n  options: { text: fixed }\n```"
	rr := newReviseRig(t, `
policy:
  agent_authored:
    allow: [ svc.* ]
`, plan, []string{revision})
	runTrigger(rr.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rr.workflowFailed(); failed {
		t.Fatalf("supervised plan must recover: %s", errStr)
	}
	var texts []string
	for _, c := range rr.fake.snapshot() {
		if c.Verb == "post" {
			texts = append(texts, fmt.Sprint(c.Opts["text"]))
		}
	}
	// "first" ran ONCE (no re-run), "never" was replaced, "fixed" ran.
	if len(texts) != 2 || texts[0] != "first" || texts[1] != "fixed" {
		t.Fatalf("splice/resume: %v", texts)
	}

	// The revise prompt carried STRUCTURED context.
	if len(rr.prompts) != 1 {
		t.Fatalf("revise rounds: %d", len(rr.prompts))
	}
	p := rr.prompts[0]
	if !strings.Contains(p, "```json") {
		t.Fatalf("revise prompt must carry structured context:\n%s", p)
	}
	blob := strings.SplitN(strings.SplitN(p, "```json\n", 2)[1], "```", 2)[0]
	var fc failureContext
	if err := json.Unmarshal([]byte(blob), &fc); err != nil {
		t.Fatalf("failure context JSON: %v\n%s", err, blob)
	}
	if fc.FailedStep != "boom" || !strings.Contains(fc.Error, "always fails") ||
		len(fc.Executed) != 1 || fc.Executed[0] != "first" || fc.Revision != 1 {
		t.Fatalf("failure context: %+v", fc)
	}
	if len(fc.Remaining) != 2 { // boom + never
		t.Fatalf("remaining steps: %+v", fc.Remaining)
	}
	if fc.Outputs["first"] == nil {
		t.Fatalf("prior outputs missing: %+v", fc.Outputs)
	}

	// Audited.
	revs := rr.Store.auditsWithEvent("plan_revise")
	if len(revs) != 1 || revs[0]["outcome"] != "spliced" {
		t.Fatalf("revise audit: %+v", revs)
	}
}

// TestSuperviseRevisionCapEscalates: after max_revisions failed rounds the
// plan compensates, escalates to needs_input, and fails — never loops.
func TestSuperviseRevisionCapEscalates(t *testing.T) {
	plan := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: ok }\n  compensate: { uses: svc.post, options: { text: undo } }\n- id: boom\n  uses: svc.fail\n  options: {}\n```"
	stillBroken := "```plan\n- id: boom2\n  uses: svc.fail\n  options: {}\n```"
	rr := newReviseRig(t, `
policy:
  agent_authored:
    allow: [ svc.* ]
    max_revisions: 2
`, plan, []string{stillBroken, stillBroken, stillBroken, stillBroken})
	runTrigger(rr.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, _ := rr.workflowFailed()
	if !failed {
		t.Fatal("exhausted revisions must fail the plan")
	}
	if len(rr.prompts) != 2 {
		t.Fatalf("revise rounds must cap at max_revisions: %d", len(rr.prompts))
	}
	// Compensation ran for the committed step; escalation fired.
	var undo bool
	for _, c := range rr.fake.snapshot() {
		if c.Verb == "post" && c.Opts["text"] == "undo" {
			undo = true
		}
	}
	if !undo {
		t.Fatal("compensation must run after the cap")
	}
	var needs int
	for _, e := range rr.Notifier.snapshot() {
		if e.Event == "needs_input" {
			needs++
		}
	}
	if needs == 0 {
		t.Fatal("cap must escalate to a human")
	}
}

// TestSuperviseNoSessionEscalatesDirectly: without a Revise service (no
// session binding), a failure goes straight to compensate + escalate.
func TestSuperviseNoSessionEscalatesDirectly(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.* ]
`)
	rig, _ := dispatchPlan(t, cfg, "```plan\n- id: boom\n  uses: svc.fail\n  options: {}\n```")
	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("must fail without a revise path")
	}
}

// TestSuperviseRevisionReguarded: a revision that smuggles in a non-allowed
// verb is rejected and does NOT run; the loop proceeds to escalation.
func TestSuperviseRevisionReguarded(t *testing.T) {
	plan := "```plan\n- id: boom\n  uses: svc.fail\n  options: {}\n```"
	sneaky := "```plan\n- id: sneak\n  uses: svc.ask\n  options: { prompt: p }\n```"
	rr := newReviseRig(t, `
policy:
  agent_authored:
    allow: [ svc.post, svc.fail ]
    max_revisions: 1
`, plan, []string{sneaky})
	runTrigger(rr.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, _ := rr.workflowFailed(); !failed {
		t.Fatal("a rejected revision must not rescue the plan")
	}
	for _, c := range rr.fake.snapshot() {
		if c.Verb == "ask" {
			t.Fatal("a non-allowed revised step must never run")
		}
	}
	revs := rr.Store.auditsWithEvent("plan_revise")
	if len(revs) == 0 || revs[0]["outcome"] != "rejected" {
		t.Fatalf("rejected revision audit: %+v", revs)
	}
}

// TestSuperviseCheckIn: a successful step marked escalate_to: agent routes
// its outputs back; a plan in the reply revises the REMAINING steps.
func TestSuperviseCheckIn(t *testing.T) {
	plan := "```plan\n" +
		"- id: probe\n  escalate_to: agent\n  uses: svc.post\n  options: { text: probe }\n" +
		"- id: old-tail\n  uses: svc.post\n  options: { text: old-tail }\n" +
		"```"
	newTail := "```plan\n- id: new-tail\n  uses: svc.post\n  options: { text: new-tail }\n```"
	rr := newReviseRig(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
`, plan, []string{newTail})
	runTrigger(rr.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rr.workflowFailed(); failed {
		t.Fatalf("check-in plan failed: %s", errStr)
	}
	var texts []string
	for _, c := range rr.fake.snapshot() {
		texts = append(texts, fmt.Sprint(c.Opts["text"]))
	}
	if len(texts) != 2 || texts[0] != "probe" || texts[1] != "new-tail" {
		t.Fatalf("check-in revise: %v", texts)
	}
	if len(rr.prompts) != 1 || !strings.Contains(rr.prompts[0], "check-in") {
		t.Fatalf("check-in prompt: %v", rr.prompts)
	}
}

// TestSuperviseCheckInContinuesWithoutPlan: a check-in reply with no plan
// block just continues as planned.
func TestSuperviseCheckInContinuesWithoutPlan(t *testing.T) {
	plan := "```plan\n" +
		"- id: probe\n  escalate_to: agent\n  uses: svc.post\n  options: { text: probe }\n" +
		"- id: tail\n  uses: svc.post\n  options: { text: tail }\n" +
		"```"
	rr := newReviseRig(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
`, plan, []string{"looks good, carry on"})
	runTrigger(rr.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rr.workflowFailed(); failed {
		t.Fatalf("failed: %s", errStr)
	}
	var texts []string
	for _, c := range rr.fake.snapshot() {
		texts = append(texts, fmt.Sprint(c.Opts["text"]))
	}
	if len(texts) != 2 || texts[1] != "tail" {
		t.Fatalf("continue-as-planned: %v", texts)
	}
}

// TestSuperviseBadEscalateTo: escalate_to takes only "agent".
func TestSuperviseBadEscalateTo(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
`)
	rig, _ := dispatchPlan(t, cfg, "```plan\n- id: s\n  escalate_to: human\n  uses: svc.post\n  options: { text: t }\n```")
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "escalate_to") {
		t.Fatalf("bad escalate_to: %v %q", failed, errStr)
	}
}

// ptr helper for config in tests.
var _ = config.AgentAuthoredPolicy{}

// REGRESSION (audit finding #8): a revision of an APPROVED plan splices —
// the original grant covers the approve-gated classes already cleared
// (including the committed steps re-seen by the full-plan re-guard) — while
// a revision introducing a NEW approval-gated class is still rejected.
func TestSuperviseApprovedPlanRevises(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post, svc.fail ]
    approve: [ svc.ask, svc.slow ]
    approve_via: svc
`
	// The original plan needs approval (svc.ask), gets it (the fake's ask
	// approves), commits the ask, then fails — the revision reuses the
	// GRANTED class (another svc.ask) plus an allowed fix.
	plan := "```plan\n" +
		"- id: risky\n  uses: svc.ask\n  options: { prompt: may I }\n" +
		"- id: boom\n  uses: svc.fail\n  options: {}\n" +
		"```"
	revision := "```plan\n- id: risky2\n  uses: svc.ask\n  options: { prompt: again }\n- id: fixed\n  uses: svc.post\n  options: { text: fixed }\n```"
	rr := newReviseRig(t, pol, plan, []string{revision})
	runTrigger(rr.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rr.workflowFailed(); failed {
		t.Fatalf("an approved plan's revision must splice, not escalate: %s", errStr)
	}
	var posts []string
	for _, c := range rr.fake.snapshot() {
		if c.Verb == "post" {
			posts = append(posts, fmt.Sprint(c.Opts["text"]))
		}
	}
	if len(posts) != 1 || posts[0] != "fixed" {
		t.Fatalf("revision must run: %v", posts)
	}
	revs := rr.Store.auditsWithEvent("plan_revise")
	if len(revs) != 1 || revs[0]["outcome"] != "spliced" {
		t.Fatalf("revise audit: %+v", revs)
	}

	// A revision smuggling a NEW approve-gated class (svc.slow — never
	// granted) is rejected.
	sneaky := "```plan\n- id: sneak\n  uses: svc.slow\n  options: {}\n```"
	rr2 := newReviseRig(t, pol, plan, []string{sneaky})
	runTrigger(rr2.testRig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, _ := rr2.workflowFailed(); !failed {
		t.Fatal("a new gated class in a revision must still reject")
	}
	revs = rr2.Store.auditsWithEvent("plan_revise")
	if len(revs) == 0 || revs[0]["outcome"] != "rejected" ||
		!strings.Contains(fmt.Sprint(revs[0]["error"]), "svc.slow") {
		t.Fatalf("new-class rejection audit: %+v", revs)
	}
	for _, c := range rr2.fake.snapshot() {
		if c.Verb == "slow" {
			t.Fatal("the smuggled verb must never run")
		}
	}
}
