package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
)

// doneRunner is a controller runtime that knows its own dispatches.
type doneRunner struct {
	fakeFlowDispatcher
	mu       sync.Mutex
	agents   map[string]string
	inflight map[string]bool
	archived []string
}

func (r *doneRunner) AgentForDispatch(d string) string { return r.agents[d] }
func (r *doneRunner) DispatchInFlight(d string) bool   { return r.inflight[d] }
func (r *doneRunner) Archive(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.archived = append(r.archived, id)
	return nil
}

// step.done from an agent a controller runtime (cli, acp) launched resolves
// through that runtime: an in-flight foreground dispatch defers to the step
// boundary, a finished one is archived by the runner that opened it, and a
// dispatch no one launched is still refused.
func TestStepDoneRoutesToTheControllerThatLaunchedIt(t *testing.T) {
	run := &doneRunner{
		agents:   map[string]string{"d-bg": "claude-code-3"},
		inflight: map[string]bool{"d-fg": true},
	}
	cfg := &config.Config{}
	eng := New(Options{Config: cfg, Store: newFlowGateStore(), Dispatch: fakeFlowDispatcher{}, Notifier: &fakeNotif{},
		Controllers: controller.NewRegistry(nil, "", run, nil)})

	if err := eng.StepDone(context.Background(), "d-fg", "", "done"); err != nil {
		t.Fatalf("in-flight controller dispatch: %v", err)
	}
	if len(run.archived) != 0 {
		t.Fatalf("an in-flight dispatch is reclaimed at the step boundary, not now: %v", run.archived)
	}
	if err := eng.StepDone(context.Background(), "d-bg", "", "done"); err != nil {
		t.Fatalf("controller dispatch: %v", err)
	}
	if len(run.archived) != 1 || run.archived[0] != "claude-code-3" {
		t.Fatalf("archived = %v, want the dispatch's own session", run.archived)
	}
	if err := eng.StepDone(context.Background(), "d-unknown", "", "done"); err == nil {
		t.Fatal("a dispatch no runtime launched must be refused")
	}
}
