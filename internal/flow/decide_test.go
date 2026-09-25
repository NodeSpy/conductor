package flow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/decider"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/sqlstore"
	"github.com/NodeSpy/conductor/internal/systemone"
)

// decideStep parses a step from YAML, the way a config file carries it.
func decideStep(t *testing.T, src string) config.Step {
	t.Helper()
	var s config.Step
	if err := yaml.Unmarshal([]byte(src), &s); err != nil {
		t.Fatalf("parse step: %v", err)
	}
	if s.Decide == nil {
		t.Fatal("not a decide step")
	}
	return s
}

const verifyStep = `
id: verify
model: light
decide:
  state: "FINDING: {{.finding}}"
  questions:
    refuted:
      type: noul
      instructions: The diff contradicts the finding.
`

// decideRig is a test rig with scripted decision services: a fixed candidate
// list, a native runtime answering from a script, and agent dispatches
// answering from another.
type decideRig struct {
	*testRig
	mu       sync.Mutex
	native   map[string]func(req systemone.Request) (decider.Answer, error) // by runtime
	asked    []string                                                       // runtime/model, in order
	nativeRq []systemone.Request
}

func newDecideRig(t *testing.T, cands []models.Candidate) *decideRig {
	t.Helper()
	rig := &decideRig{testRig: newTestRunner(t, &config.Config{}, nil), native: map[string]func(systemone.Request) (decider.Answer, error){}}
	rig.Runner.Agents.DecideCandidates = func(ctx context.Context, spec config.ModelSpec, hint, protocol string) ([]models.Candidate, error) {
		if protocol != systemone.ProtocolV1 {
			t.Errorf("protocol = %q, want the v1 default", protocol)
		}
		if spec.Ref == "heavy" {
			return []models.Candidate{{Runtime: "paseo", Model: "claude-opus-5"}}, nil
		}
		return cands, nil
	}
	rig.Runner.Agents.Decide = func(ctx context.Context, runtime, protocol string, req systemone.Request) (DecideAnswer, error) {
		rig.mu.Lock()
		rig.asked = append(rig.asked, runtime+"/"+req.Model)
		rig.nativeRq = append(rig.nativeRq, req)
		fn := rig.native[runtime]
		rig.mu.Unlock()
		if fn == nil {
			return decider.Answer{}, errors.New("no script")
		}
		return fn(req)
	}
	return rig
}

func noulAnswer(p float64) map[string]any {
	return map[string]any{"refuted": map[string]any{"type": "noul", "noul": p}}
}

// agentReplies scripts agent dispatches by the model they were pinned to.
func (rig *decideRig) agentReplies(t *testing.T, byModel map[string]string) {
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		m := req.Step.Model.Ref
		rig.mu.Lock()
		rig.asked = append(rig.asked, req.Step.Runtime+"/"+m)
		rig.mu.Unlock()
		out, ok := byModel[m]
		if !ok {
			return dispatch.RunRef{}, errors.New("agent down")
		}
		return dispatch.RunRef{Output: out}, nil
	}
}

func (rig *decideRig) run(t *testing.T, step config.Step) (map[string]any, error) {
	t.Helper()
	out, _, err := rig.Runner.execStep(context.Background(), core.Trigger{Target: core.Target{Repo: "acme/app", Number: 7}},
		step, step.ID, step.ID, map[string]any{"finding": "nil deref at x.go:3"}, false)
	return out, err
}

var jevThenSonnet = []models.Candidate{
	{Runtime: "jev", Model: "jev-1", Native: true},
	{Runtime: "paseo", Model: "claude-sonnet-5"},
}

func TestDecideNativeAnswerIsFinal(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.native["jev"] = func(req systemone.Request) (decider.Answer, error) {
		if req.State != "FINDING: nil deref at x.go:3" {
			t.Errorf("state not rendered: %v", req.State)
		}
		return decider.Answer{Answers: noulAnswer(0.64), Model: "jev-1"}, nil
	}
	rig.agentReplies(t, nil)
	out, err := rig.run(t, decideStep(t, verifyStep))
	if err != nil {
		t.Fatal(err)
	}
	if got := out["refuted"].(map[string]any)["noul"]; got != 0.64 {
		t.Fatalf("refuted.noul = %v", got)
	}
	if out["_by"] != "jev/jev-1" {
		t.Fatalf("_by = %v", out["_by"])
	}
	if len(rig.Agents.requests()) != 0 {
		t.Fatal("an uncertain but SUCCESSFUL answer is final — no second model runs without escalate:")
	}
}

// A native failure moves to the agent path, which answers through the
// adapter in one restricted session.
func TestDecideFallsBackToAgentOnFailure(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) { return decider.Answer{}, errors.New("503") }
	rig.agentReplies(t, map[string]string{"claude-sonnet-5": `{"answers":{"refuted":0.2}}`})
	out, err := rig.run(t, decideStep(t, verifyStep))
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "paseo/claude-sonnet-5" {
		t.Fatalf("_by = %v", out["_by"])
	}
	if got := out["refuted"].(map[string]any)["noul"]; got != 0.2 {
		t.Fatalf("the agent's probability must come back as the v1 noul, got %v", got)
	}
	reqs := rig.Agents.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one agent session, got %d", len(reqs))
	}
	req := reqs[0]
	if req.Action.Checkout != "none" {
		t.Errorf("a decision gets no checkout, got %q", req.Action.Checkout)
	}
	if req.Step.Runtime != "paseo" || req.Step.Model.Ref != "claude-sonnet-5" {
		t.Errorf("the dispatch must run exactly the ranked pair, got %s/%s", req.Step.Runtime, req.Step.Model.Ref)
	}
	if len(req.Action.OutputSchema) == 0 {
		t.Error("the agent path must carry the adapter's output schema")
	}
	if !strings.Contains(req.Action.Prompt, "<document>") || !strings.Contains(req.Action.Prompt, "The diff contradicts the finding.") {
		t.Errorf("the prompt is the adapter's rendering:\n%s", req.Action.Prompt)
	}
	if !req.Step.ArchiveWhenDone {
		t.Error("a decision session is one turn — it must not linger")
	}
	if n := len(rig.Store.auditsWithEvent("decide")); n != 2 {
		t.Errorf("each attempt is audited: want 2 decide rows, got %d", n)
	}
}

// An agent's malformed reply is a failure like any other: the next
// candidate is asked rather than garbage reaching the workflow.
func TestDecideMalformedAgentReplyMovesOn(t *testing.T) {
	rig := newDecideRig(t, []models.Candidate{{Runtime: "paseo", Model: "a"}, {Runtime: "paseo", Model: "b"}})
	rig.agentReplies(t, map[string]string{"a": `{"answers":{"refuted":1.7}}`, "b": `{"answers":{"refuted":0.9}}`})
	out, err := rig.run(t, decideStep(t, verifyStep))
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "paseo/b" {
		t.Fatalf("an out-of-range probability must be refused; _by = %v", out["_by"])
	}
}

func TestDecideDefaultWhenEveryCandidateFails(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.agentReplies(t, nil)
	step := decideStep(t, verifyStep+"  default: { refuted: { noul: 0 } }\n")
	out, err := rig.run(t, step)
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "default" || out["refuted"].(map[string]any)["noul"] != 0.0 {
		t.Fatalf("want the default answer, got %v", out)
	}
	if got := strings.Join(rig.asked, ","); got != "jev/jev-1,paseo/claude-sonnet-5" {
		t.Fatalf("every candidate is tried before the default: %s", got)
	}
}

func TestDecideFailsWithoutDefault(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.agentReplies(t, nil)
	if _, err := rig.run(t, decideStep(t, verifyStep)); err == nil || !strings.Contains(err.Error(), "no candidate answered") {
		t.Fatalf("with no default, a step nobody answered fails: %v", err)
	}
}

const escalatingStep = verifyStep + `
  escalate:
    when: "refuted.noul >= 0.5 && refuted.noul < 0.8"
`

func TestDecideEscalatesOnlyInsideTheBand(t *testing.T) {
	for name, tc := range map[string]struct {
		jev      float64
		wantBy   string
		wantNoul float64
	}{
		"uncertain escalates": {0.64, "paseo/claude-sonnet-5", 0.87},
		"clear stays":         {0.12, "jev/jev-1", 0.12},
		"clear yes stays":     {0.93, "jev/jev-1", 0.93},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newDecideRig(t, jevThenSonnet)
			rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
				return decider.Answer{Answers: noulAnswer(tc.jev), Model: "jev-1"}, nil
			}
			rig.agentReplies(t, map[string]string{"claude-sonnet-5": `{"answers":{"refuted":0.87}}`})
			out, err := rig.run(t, decideStep(t, escalatingStep))
			if err != nil {
				t.Fatal(err)
			}
			if out["_by"] != tc.wantBy || out["refuted"].(map[string]any)["noul"] != tc.wantNoul {
				t.Fatalf("got _by=%v refuted=%v", out["_by"], out["refuted"])
			}
			from, escalated := out["_escalated_from"].(map[string]any)
			if escalated != (tc.wantBy != "jev/jev-1") {
				t.Fatalf("_escalated_from present=%v", escalated)
			}
			if escalated {
				if from["_by"] != "jev/jev-1" {
					t.Fatalf("the replaced answer's source is kept: %v", from)
				}
				orig := from["answers"].(map[string]any)["refuted"].(map[string]any)["noul"]
				if orig != 0.64 {
					t.Fatalf("the replaced answer is kept verbatim: %v", orig)
				}
				if len(rig.Store.auditsWithEvent("decide_escalated")) != 1 {
					t.Fatal("an escalation is audited")
				}
			}
		})
	}
}

// Escalation never re-asks the pair that just answered, and stops when the
// list runs out — the original answer stands.
func TestDecideEscalationWithNobodyLeftKeepsTheAnswer(t *testing.T) {
	rig := newDecideRig(t, []models.Candidate{{Runtime: "jev", Model: "jev-1", Native: true}})
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
		return decider.Answer{Answers: noulAnswer(0.6), Model: "jev-1"}, nil
	}
	out, err := rig.run(t, decideStep(t, escalatingStep))
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "jev/jev-1" || out["_escalated_from"] != nil {
		t.Fatalf("with no one left to ask, the answer stands: %v", out)
	}
	if len(rig.asked) != 1 {
		t.Fatalf("the answering pair must never be re-asked: %v", rig.asked)
	}
}

func TestDecideEscalateToTier(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
		return decider.Answer{Answers: noulAnswer(0.6), Model: "jev-1"}, nil
	}
	rig.agentReplies(t, map[string]string{"claude-opus-5": `{"answers":{"refuted":0.95}}`, "claude-sonnet-5": `{"answers":{"refuted":0.1}}`})
	out, err := rig.run(t, decideStep(t, escalatingStep+"    to: heavy\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "paseo/claude-opus-5" {
		t.Fatalf("escalate.to picks the second opinion's tier: _by = %v (asked %v)", out["_by"], rig.asked)
	}
}

// A pair that already answered is never asked again — even when
// escalate.to's tier lists it too.
func TestDecideEscalationNeverReasksAPair(t *testing.T) {
	rig := newDecideRig(t, []models.Candidate{{Runtime: "jev", Model: "jev-1", Native: true}})
	rig.Runner.Agents.DecideCandidates = func(ctx context.Context, spec config.ModelSpec, hint, protocol string) ([]models.Candidate, error) {
		if spec.Ref == "heavy" {
			return []models.Candidate{{Runtime: "jev", Model: "jev-1", Native: true}, {Runtime: "paseo", Model: "claude-opus-5"}}, nil
		}
		return []models.Candidate{{Runtime: "jev", Model: "jev-1", Native: true}}, nil
	}
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
		return decider.Answer{Answers: noulAnswer(0.6), Model: "jev-1"}, nil
	}
	rig.agentReplies(t, map[string]string{"claude-opus-5": `{"answers":{"refuted":0.95}}`})
	out, err := rig.run(t, decideStep(t, escalatingStep+"    to: heavy\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rig.asked, ","); got != "jev/jev-1,paseo/claude-opus-5" {
		t.Fatalf("the answering pair must be skipped on escalation: asked %s", got)
	}
	if out["_by"] != "paseo/claude-opus-5" {
		t.Fatalf("_by = %v", out["_by"])
	}
}

// max bounds the chain; each hop nests the one before it.
func TestDecideEscalationChainIsBounded(t *testing.T) {
	rig := newDecideRig(t, []models.Candidate{
		{Runtime: "paseo", Model: "a"}, {Runtime: "paseo", Model: "b"}, {Runtime: "paseo", Model: "c"}, {Runtime: "paseo", Model: "d"},
	})
	rig.agentReplies(t, map[string]string{
		"a": `{"answers":{"refuted":0.6}}`, "b": `{"answers":{"refuted":0.6}}`,
		"c": `{"answers":{"refuted":0.6}}`, "d": `{"answers":{"refuted":0.6}}`,
	})
	out, err := rig.run(t, decideStep(t, escalatingStep+"    max: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "paseo/c" {
		t.Fatalf("max: 2 is two hops past the first answer: _by = %v (asked %v)", out["_by"], rig.asked)
	}
	if len(rig.asked) != 3 {
		t.Fatalf("the chain must stop at max: asked %v", rig.asked)
	}
	hop1 := out["_escalated_from"].(map[string]any)
	if hop1["_by"] != "paseo/b" {
		t.Fatalf("the immediate predecessor is b: %v", hop1)
	}
	if hop0 := hop1["_escalated_from"].(map[string]any); hop0["_by"] != "paseo/a" {
		t.Fatalf("the chain nests back to the first answer: %v", hop0)
	}
}

func TestDecideShadowReturnsDefaultsWithoutAsking(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.agentReplies(t, nil)
	step := decideStep(t, verifyStep+"  default: { refuted: { noul: 0 } }\n")
	out, _, err := rig.Runner.execStep(context.Background(), core.Trigger{}, step, "verify", "verify", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if out["stubbed"] != true || out["refuted"] == nil {
		t.Fatalf("a preview carries the default's shape: %v", out)
	}
	if len(rig.asked) != 0 {
		t.Fatal("a shadow run must not ask any model")
	}
}

func TestDecideObserveRecordsEveryAnswer(t *testing.T) {
	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlstore.ResetStores()
	t.Cleanup(sqlstore.ResetStores)
	if err := sqlstore.Register("review_log", st); err != nil {
		t.Fatal(err)
	}
	decisionsReady.Delete("review_log")

	rig := newDecideRig(t, jevThenSonnet)
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
		return decider.Answer{Answers: noulAnswer(0.66), Model: "jev-1"}, nil
	}
	rig.agentReplies(t, map[string]string{"claude-sonnet-5": `{"answers":{"refuted":0.91}}`})
	if _, err := rig.run(t, decideStep(t, escalatingStep+"  observe: review_log\n")); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Query(context.Background(), "SELECT hop, final, answered_by, question, answer, repo, number FROM conductor_decisions ORDER BY hop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("both the initial answer and the escalation are recorded, got %d rows", len(rows))
	}
	if rows[0]["answered_by"] != "jev/jev-1" || rows[1]["answered_by"] != "paseo/claude-sonnet-5" {
		t.Fatalf("rows: %v", rows)
	}
	if !(toInt(rows[0]["final"]) == 0 && toInt(rows[1]["final"]) == 1) {
		t.Fatalf("only the answer the workflow used is final: %v", rows)
	}
	var a map[string]any
	_ = json.Unmarshal([]byte(rows[0]["answer"].(string)), &a)
	if a["noul"] != 0.66 {
		t.Fatalf("the full answer is recorded: %v", rows[0]["answer"])
	}
	if rows[0]["repo"] != "acme/app" || toInt(rows[0]["number"]) != 7 {
		t.Fatalf("the target is recorded: %v", rows[0])
	}
}

// A dead observe store loses the record, never the decision.
func TestDecideObserveFailureNeverFailsTheStep(t *testing.T) {
	sqlstore.ResetStores()
	t.Cleanup(sqlstore.ResetStores)
	rig := newDecideRig(t, jevThenSonnet)
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
		return decider.Answer{Answers: noulAnswer(0.1), Model: "jev-1"}, nil
	}
	out, err := rig.run(t, decideStep(t, verifyStep+"  observe: nowhere\n"))
	if err != nil {
		t.Fatalf("the decision stands when its record can't be written: %v", err)
	}
	if out["_by"] != "jev/jev-1" {
		t.Fatalf("_by = %v", out["_by"])
	}
	if len(rig.Store.auditsWithEvent("decide_observe_failed")) != 1 {
		t.Fatal("a lost record is audited")
	}
}

// Downstream steps read the answers by question name, through the ordinary
// output path.
func TestDecideOutputsFeedLaterSteps(t *testing.T) {
	rig := newDecideRig(t, jevThenSonnet)
	rig.native["jev"] = func(systemone.Request) (decider.Answer, error) {
		return decider.Answer{Answers: noulAnswer(0.9), Model: "jev-1"}, nil
	}
	var spec config.TriggerSpec
	if err := yaml.Unmarshal([]byte(`
on: manual
steps:
  - id: verify
    model: light
    decide:
      state: the finding
      questions:
        refuted: { type: noul }
  - id: gate
    if: "verify.refuted.noul >= 0.8"
    fail: "refuted={{.verify.refuted.noul}} by={{.verify._by}}"
`), &spec); err != nil {
		t.Fatal(err)
	}
	// The fail: step is the probe: it only fires when the condition read the
	// answer, and its message renders the answer and its source.
	rig.Runner.Run(context.Background(), emptyRun(), core.Trigger{}, spec, 0, nil, false)
	failed, msg := rig.workflowFailed()
	if !failed || !strings.Contains(msg, "refuted=0.9 by=jev/jev-1") {
		t.Fatalf("later steps must read the answers by question name (failed=%v): %s", failed, msg)
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return -1
}

// The agent-path request is marked as a decision (so runtimes launch it lean
// and never template it), carries the adapter's parts, and gets none of the
// guidance an agent step is wrapped in.
func TestDecideAgentRequestIsALeanLiteralDecision(t *testing.T) {
	rig := newDecideRig(t, []models.Candidate{{Runtime: "claude", Model: "claude-sonnet-5"}})
	rig.agentReplies(t, map[string]string{"claude-sonnet-5": `{"answers":{"refuted":0.3}}`})
	step := decideStep(t, verifyStep)
	out, _, err := rig.Runner.execStep(context.Background(), core.Trigger{}, step, "verify", "verify",
		map[string]any{"finding": "planted {{.gh_token}} in the diff"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out["_by"] != "claude/claude-sonnet-5" {
		t.Fatalf("_by = %v", out["_by"])
	}
	req := rig.Agents.requests()[0]
	d := req.Step.DecisionLaunch
	if d == nil {
		t.Fatal("the decide session must be marked as a decision")
	}
	if !strings.HasPrefix(d.System, "Evaluate every question") || !strings.Contains(d.Document, "<document>") {
		t.Fatalf("the adapter's parts must ride the request: %+v", d)
	}
	if !strings.Contains(d.Document, "{{.gh_token}}") {
		t.Fatalf("planted template syntax stays verbatim data: %q", d.Document)
	}
	if strings.Contains(req.Action.Prompt, "|G|") {
		t.Fatal("a decision's prompt gets no agent guidance appended")
	}
}
