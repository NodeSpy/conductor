package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// closeMidDispatch is a runtime with no StopTarget whose agent escapes the
// stop: the PR closes in the very instant the agent launches, and the agent
// returns later, its work committed in its workspace.
type closeMidDispatch struct {
	fakeFlowDispatcher
	onDispatch func(dispatch.Request)
	workdir    string
}

func (d *closeMidDispatch) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	d.onDispatch(req)
	return dispatch.RunRef{Output: "{}", AgentID: "a1", Workdir: d.workdir}, nil
}

const closedPushCfg = `
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    hooks:
      - { at: done, uses: eg.post, options: { text: "tcp-done" } }
      - { at: fail, uses: eg.post, options: { text: "tcp-fail" } }
      - { at: stop, uses: eg.post, options: { text: "tcp-stop" } }
    steps:
      - { id: fix, type: agent, prompt: "fix it" }
      - { id: after, uses: eg.post, options: { text: "tcp-after" } }
`

// The live sequence: a failing-checks fixer dispatches and the PR merges in
// the same instant; the agent comes back minutes later with commits. Nothing
// is pushed, no later step runs, the run ends stopped with its stop hooks —
// not its done or fail hooks — and a git.push the agent makes after the
// close is refused at the push gate.
func TestClosedPRMidDispatchPushesNothing(t *testing.T) {
	d := &closeMidDispatch{workdir: t.TempDir()}
	eng, st, _, _ := buildFlowEngineWithDispatcher(t, closedPushCfg, d)
	var pushes atomic.Int32
	eng.flow.PushWork = func(_ context.Context, id string) (map[string]any, error) {
		if err := eng.flow.PushRefusal(id); err != nil {
			return nil, err
		}
		pushes.Add(1)
		return map[string]any{"pushed": true}, nil
	}
	var mu sync.Mutex
	var dispatchID string
	closed := flowTrigger("")
	closed.Kind = "_closed"
	closed.Action = nil
	d.onDispatch = func(req dispatch.Request) {
		mu.Lock()
		dispatchID = req.DispatchID
		mu.Unlock()
		eng.process(context.Background(), closed) // merged as the agent starts
	}

	fix := flowTrigger("tcp-fix")
	fix.Kind = "new_comment"
	if !fix.BoundToTarget() {
		t.Fatal("fixture: the fixer's trigger must be bound to its target")
	}
	eng.process(context.Background(), fix)
	waitCond(t, "the stop hook", func() bool { return gateCallsWith("tcp-stop") == 1 })
	time.Sleep(50 * time.Millisecond)

	if n := pushes.Load(); n != 0 {
		t.Fatalf("pushed %d time(s) after the PR closed", n)
	}
	for _, s := range []string{"tcp-after", "tcp-done", "tcp-fail"} {
		if n := gateCallsWith(s); n != 0 {
			t.Fatalf("%s ran after the PR closed", s)
		}
	}
	st.mu.Lock()
	stopped := 0
	for _, a := range st.audits {
		if a["event"] == "workflow_stopped" {
			stopped++
		}
	}
	st.mu.Unlock()
	if stopped != 1 {
		t.Fatalf("workflow_stopped audits = %d, want 1", stopped)
	}

	// The agent's own git.push, after the close: refused at the gate.
	mu.Lock()
	id := dispatchID
	mu.Unlock()
	if id == "" {
		t.Fatal("no dispatch id recorded")
	}
	if err := eng.flow.PushRefusal(id); err == nil {
		t.Fatal("a push for a dispatch whose PR closed under it was not refused")
	}
}

// The push gate counts a close only against work that began before it: a
// dispatch after the PR was reopened pushes.
func TestPushGateAfterReopen(t *testing.T) {
	eng, _, _, _ := buildFlowEngine(t, gateCfg)
	fix := flowTrigger("")
	fix.Kind = "new_comment"
	eng.noteDispatchTarget(context.Background(), "before", fix)
	eng.markClosed(fix.Key())
	time.Sleep(2 * time.Millisecond)
	eng.noteDispatchTarget(context.Background(), "after", fix)
	if !eng.dispatchTargetClosed("before") {
		t.Fatal("work begun before the close is still pushable")
	}
	if eng.dispatchTargetClosed("after") {
		t.Fatal("work begun after a reopen is refused")
	}
	if eng.dispatchTargetClosed("unknown") {
		t.Fatal("an unknown dispatch is refused")
	}
}

// A bound run registered after the close it waited through starts stopped;
// one registered before is cancelled by the close.
func TestBindRunToTarget(t *testing.T) {
	eng, _, _, _ := buildFlowEngine(t, gateCfg)
	fix := flowTrigger("")
	fix.Kind = "new_comment"
	queued := time.Now()
	running, unbind := eng.bindRunToTarget(context.Background(), fix, queued)
	defer unbind()
	if n := eng.markClosed(fix.Key()); n != 1 {
		t.Fatalf("markClosed stopped %d runs, want 1", n)
	}
	if context.Cause(running) != dispatch.ErrTargetClosed {
		t.Fatalf("running run's cause = %v", context.Cause(running))
	}
	waited, unbind2 := eng.bindRunToTarget(context.Background(), fix, queued)
	defer unbind2()
	if context.Cause(waited) != dispatch.ErrTargetClosed {
		t.Fatal("a run that waited through the close did not start stopped")
	}
	other := flowTrigger("")
	other.Kind = "ping" // not bound to its target
	free, unbind3 := eng.bindRunToTarget(context.Background(), other, queued)
	defer unbind3()
	if free.Err() != nil {
		t.Fatal("an unbound run was stopped")
	}
}

// buildFlowEngineWithDispatcher is buildFlowEngineWith over any runtime.
func buildFlowEngineWithDispatcher(t *testing.T, cfgYAML string, d Dispatcher) (*Engine, *flowGateStore, *fakeNotif, *config.Config) {
	t.Helper()
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	st := newFlowGateStore()
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	eng := New(Options{Config: &cfg, Store: st, Dispatch: d, Notifier: notif, Flow: runner, Connectors: reg})
	return eng, st, notif, &cfg
}
