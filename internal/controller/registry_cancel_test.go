package controller

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// fakePaseoRunner satisfies both Runner and paseoCanceller — the shape the
// built-in paseo controller's Runner has in production (*dispatch.Dispatcher).
type fakePaseoRunner struct {
	agents   map[string][]dispatch.AgentInfo // "pr" label value -> its agents
	archived []string
}

func (f *fakePaseoRunner) Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error) {
	return dispatch.RunRef{}, nil
}
func (f *fakePaseoRunner) WaitForAgent(context.Context, string, time.Duration) {}
func (f *fakePaseoRunner) HasLiveAgent(context.Context, string, string) bool   { return false }
func (f *fakePaseoRunner) Archive(_ context.Context, id string) error {
	f.archived = append(f.archived, id)
	return nil
}
func (f *fakePaseoRunner) AgentForDispatch(string) string          { return "" }
func (f *fakePaseoRunner) DispatchInFlight(string) bool            { return false }
func (f *fakePaseoRunner) DeliverOutput(string, any) (bool, error) { return false, nil }
func (f *fakePaseoRunner) ListAgents(_ context.Context, labels map[string]string) ([]dispatch.AgentInfo, error) {
	return f.agents[labels["pr"]], nil
}

// TestRegistryCancelTargetAcrossControllersAndPaseo proves Registry.CancelTarget
// walks EVERY registered controller: a controllerRunner-backed one (ACP here,
// reached via its own CancelTarget) and the built-in paseo default (reached via
// the ListAgents+Archive fallback, since the paseo Runner carries no
// controller-side liveness table of its own).
func TestRegistryCancelTargetAcrossControllersAndPaseo(t *testing.T) {
	prKey := "acme/app#1"
	paseo := &fakePaseoRunner{agents: map[string][]dispatch.AgentInfo{
		prKey: {{ID: "pagent-1"}},
	}}
	cfgs := map[string]config.ControllerConfig{
		"gem": {Agent: "gemini"}, // ACP transport → controllerRunner-backed
	}
	reg := NewRegistry(cfgs, "", paseo, nil)

	runner, err := reg.RunnerFor("gem")
	if err != nil {
		t.Fatal(err)
	}
	cr, ok := runner.(*controllerRunner)
	if !ok {
		t.Fatalf("gem's runner = %T, want *controllerRunner", runner)
	}
	sess := &stubSession{id: "acp-1"}
	cr.mu.Lock()
	cr.live["acp-1"] = sess
	cr.bucket["acp-1"] = prKey + "\x00merge_conflict"
	cr.byPR[prKey+"\x00merge_conflict"] = 1
	cr.mu.Unlock()

	ids := reg.CancelTarget(context.Background(), prKey, "target merged")
	sort.Strings(ids)
	want := []string{"acp-1", "pagent-1"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("CancelTarget ids = %v, want %v", ids, want)
	}
	if !sess.closed {
		t.Fatal("the ACP session should have been closed")
	}
	if len(paseo.archived) != 1 || paseo.archived[0] != "pagent-1" {
		t.Fatalf("paseo archived = %v, want [pagent-1]", paseo.archived)
	}
}

// TestRegistryCancelTargetNoAgentsIsNoop proves an unaffected target archives
// nothing on either side.
func TestRegistryCancelTargetNoAgentsIsNoop(t *testing.T) {
	paseo := &fakePaseoRunner{agents: map[string][]dispatch.AgentInfo{}}
	reg := NewRegistry(nil, "", paseo, nil)
	ids := reg.CancelTarget(context.Background(), "acme/app#9", "target closed")
	if len(ids) != 0 {
		t.Fatalf("CancelTarget = %v, want none", ids)
	}
}
