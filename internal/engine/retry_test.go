package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// retryCfg: two verb steps; the second consumes the first's output — the
// retry must PIN the recorded output instead of re-running step one.
const retryCfg = `
connectors:
  eg: { type: enginegate }
triggers:
  - on: eg.ping
    steps:
      - { id: first,  uses: eg.post, options: { text: "one" } }
      - { id: second, uses: eg.post, options: { text: "got {{.first.token}}" } }
`

// recordedRun builds the history record of a prior execution: step one
// succeeded (with an output), step two failed.
func recordedRun(t *testing.T) store.RunHistory {
	t.Helper()
	trig := flowTrigger("d1")
	tp := trig
	tp.Action = nil
	trigRaw, err := json.Marshal(tp)
	if err != nil {
		t.Fatal(err)
	}
	actRaw, err := json.Marshal(config.Action{FlowRef: "0:eg.ping"})
	if err != nil {
		t.Fatal(err)
	}
	return store.RunHistory{
		ID: "rprev", RunID: "ping:acme/x#1", Kind: "ping", On: "eg.ping",
		Repo: "acme/x", Number: 1, Started: time.Now().Add(-time.Hour),
		Status: "failed", Error: `step "second": boom`, FailedStep: "second",
		Trigger: trigRaw, Action: actRaw,
		Steps: []store.StepRecord{
			{ID: "first", Index: 0, Status: "ok", Outputs: map[string]any{"token": "pinned-42"}},
			{ID: "second", Index: 1, Status: "failed", Error: "boom"},
		},
	}
}

// waitGateCalls polls until the async retry's verb calls land.
func waitGateCalls(t *testing.T, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		gateConnMu.Lock()
		n := len(gateConnCalls)
		gateConnMu.Unlock()
		if n >= want {
			gateConnMu.Lock()
			out := append([]map[string]any(nil), gateConnCalls...)
			gateConnMu.Unlock()
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gate calls never reached %d", want)
	return nil
}

func resetGateCalls() {
	gateConnMu.Lock()
	gateConnCalls = nil
	gateConnMu.Unlock()
}

func TestRetryFromFailedStepPinsRecordedInputs(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, retryCfg)
	resetGateCalls()
	rec := recordedRun(t)
	_ = st.PutHistory(rec)

	msg, err := eng.RetryRunByID(context.Background(), "rprev", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "step second") {
		t.Fatalf("ack: %q", msg)
	}
	calls := waitGateCalls(t, 1)
	// Only the failed step re-ran, and it rendered against the PINNED output
	// of the recorded first step — which itself never re-ran.
	if len(calls) != 1 || calls[0]["text"] != "got pinned-42" {
		t.Fatalf("retried call: %+v", calls)
	}

	// The retry produced its own history record, backlinked to the original.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		var linked *store.RunHistory
		for id, r := range st.history {
			if id != "rprev" && r.RetryOf == "rprev" && r.Status == "ok" {
				rr := r
				linked = &rr
			}
		}
		st.mu.Unlock()
		if linked != nil {
			if s, ok := linked.Step("second"); !ok || s.Status != "ok" {
				t.Fatalf("retry record steps: %+v", linked.Steps)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no backlinked retry record appeared")
}

func TestRetryFromExplicitStepAndErrors(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, retryCfg)
	resetGateCalls()
	rec := recordedRun(t)
	rec.ID = "rexpl"
	_ = st.PutHistory(rec)

	// Explicit --from first: BOTH steps re-run (the pin is only for earlier steps).
	if _, err := eng.RetryRunByID(context.Background(), "rexpl", "first"); err != nil {
		t.Fatal(err)
	}
	calls := waitGateCalls(t, 2)
	if calls[0]["text"] != "one" || calls[1]["text"] == "got pinned-42" {
		t.Fatalf("full re-run from first: %+v", calls)
	}

	// Error paths.
	if _, err := eng.RetryRunByID(context.Background(), "ghost", ""); err == nil ||
		!strings.Contains(err.Error(), "no recorded run") {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := eng.RetryRunByID(context.Background(), "rexpl", "nope"); err == nil ||
		!strings.Contains(err.Error(), `no step "nope"`) {
		t.Fatalf("unknown step: %v", err)
	}
	running := rec
	running.ID = "rlive"
	running.Status = "running"
	_ = st.PutHistory(running)
	if _, err := eng.RetryRunByID(context.Background(), "rlive", ""); err == nil ||
		!strings.Contains(err.Error(), "still running") {
		t.Fatalf("running: %v", err)
	}
	bare := store.RunHistory{ID: "rbare", Status: "failed"}
	_ = st.PutHistory(bare)
	if _, err := eng.RetryRunByID(context.Background(), "rbare", ""); err == nil ||
		!strings.Contains(err.Error(), "no retryable trigger") {
		t.Fatalf("bare: %v", err)
	}
}

// gate revisions (#36 §16): the engine routes a follow-up to the SAME agent —
// a paseo runner takes a captured send; runtimes without the capability
// report ok=false so the gate escalates instead of guessing.
type capturingDispatcher struct {
	fakeDispatcher
	sends []string
}

func (c *capturingDispatcher) SendCapture(_ context.Context, id, prompt string) (string, error) {
	c.sends = append(c.sends, id+"|"+prompt)
	return `{"note":"revised"}`, nil
}

func TestAgentFollowUpRoutes(t *testing.T) {
	d := &capturingDispatcher{}
	// Build the engine ON the capture-capable dispatcher: runnerFor resolves
	// the default profile to the built-in paseo runner, which is exactly it.
	e := New(Options{Config: baseCfg(), Store: tempStore(t), Dispatch: d,
		Notifier: &fakeNotifier{}, UserToken: func() (string, error) { return "u", nil }})

	tr := agentTrigger("fix", "o/r", 1, "h", "s", config.Action{Type: "agent", Agent: "fixer"})
	out, ok, err := e.agentFollowUp(context.Background(), "agent-9", "fixer", tr, "please fix the tests")
	if err != nil || !ok || !strings.Contains(out, "revised") {
		t.Fatalf("paseo follow-up: %q %v %v", out, ok, err)
	}
	if len(d.sends) != 1 || !strings.HasPrefix(d.sends[0], "agent-9|") {
		t.Fatalf("send capture: %v", d.sends)
	}
	// No agent id and no bound session → honest ok=false.
	if _, ok, _ := e.agentFollowUp(context.Background(), "", "fixer", tr, "x"); ok {
		t.Fatal("no transport must report ok=false")
	}
}
