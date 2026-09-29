package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/internal/targets"
)

// fakeCancelDispatcher is the engine Dispatcher AND the paseo-shaped
// ListAgents/Archive fallback controller.Registry.CancelTarget reaches when a
// Runner carries no controller-side liveness table of its own (see
// internal/controller's paseoCanceller) — the built-in paseo default in these
// tests IS this dispatcher (Options.Controllers is left nil, so engine.New
// builds a registry with it as the paseo runner).
type fakeCancelDispatcher struct {
	mu       sync.Mutex
	agents   map[string][]dispatch.AgentInfo // "pr" label value -> its agents
	archived []string
}

func (d *fakeCancelDispatcher) Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error) {
	return dispatch.RunRef{}, nil
}
func (d *fakeCancelDispatcher) WaitForAgent(context.Context, string, time.Duration) {}
func (d *fakeCancelDispatcher) HasLiveAgent(context.Context, string, string) bool   { return false }
func (d *fakeCancelDispatcher) Archive(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.archived = append(d.archived, id)
	return nil
}
func (d *fakeCancelDispatcher) AgentForDispatch(string) string          { return "" }
func (d *fakeCancelDispatcher) DispatchInFlight(string) bool            { return false }
func (d *fakeCancelDispatcher) DeliverOutput(string, any) (bool, error) { return false, nil }
func (d *fakeCancelDispatcher) ListAgents(_ context.Context, labels map[string]string) ([]dispatch.AgentInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agents[labels["pr"]], nil
}
func (d *fakeCancelDispatcher) archivedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.archived...)
}

// buildCancelEngine wires an engine exactly like buildFlowEngine (flow_test.go),
// but over an arbitrary Dispatcher rather than the fixed fakeFlowDispatcher
// type, so a test can inject one that also plays the paseo-shaped
// agent-lister/archiver.
func buildCancelEngine(t *testing.T, cfgYAML string, d Dispatcher) (*Engine, *flowGateStore, *fakeNotif) {
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
	return eng, st, notif
}

// TestClosedTriggerCancelsLiveAgentsAndRefusesFurtherWrites drives the whole
// incident end to end: a trusted `_closed` trigger (merged and closed-unmerged)
// must cancel the target's live agent, leave a "cancelled" audit row and
// notify event, and — because it also marks the target closed in
// internal/targets — refuse a subsequent write check against it.
func TestClosedTriggerCancelsLiveAgentsAndRefusesFurtherWrites(t *testing.T) {
	for _, merged := range []bool{true, false} {
		name, wantReason, wantOutcome := "merged", "target merged", "merged"
		if !merged {
			name, wantReason, wantOutcome = "closed", "target closed", "closed"
		}
		t.Run(name, func(t *testing.T) {
			repo := "acme/cancel-" + name
			number := 100
			prKey := repo + "#" + strconv.Itoa(number)
			d := &fakeCancelDispatcher{agents: map[string][]dispatch.AgentInfo{
				prKey: {{ID: "agent-1"}},
			}}
			eng, st, notif := buildCancelEngine(t, gateCfg2(), d)
			st.RecordEngagement(prKey, store.Engagement{Key: "fixer"})

			eng.process(context.Background(), closedTrigger(repo, number, merged, nil))

			if got := d.archivedIDs(); len(got) != 1 || got[0] != "agent-1" {
				t.Fatalf("archived agents = %v, want [agent-1]", got)
			}

			st.mu.Lock()
			var cancelledRow map[string]any
			for _, a := range st.audits {
				if a["event"] == "cancelled" {
					cancelledRow = a
				}
			}
			st.mu.Unlock()
			if cancelledRow == nil {
				t.Fatalf("expected a \"cancelled\" audit row, got: %+v", st.audits)
			}
			if cancelledRow["reason"] != wantReason {
				t.Fatalf("cancelled row reason = %v, want %q", cancelledRow["reason"], wantReason)
			}
			if cancelledRow["repo"] != repo || cancelledRow["number"] != number {
				t.Fatalf("cancelled row repo/number = %v/%v, want %s/%d", cancelledRow["repo"], cancelledRow["number"], repo, number)
			}

			notif.mu.Lock()
			foundNotify := false
			for _, e := range notif.events {
				if strings.HasPrefix(e, "cancelled:") && strings.Contains(e, wantReason) {
					foundNotify = true
				}
			}
			notif.mu.Unlock()
			if !foundNotify {
				t.Fatalf("expected a cancelled notify event, got %v", notif.events)
			}

			// A subsequent write check against the now-closed target is refused.
			outcome, ok := targets.Default.Closed(repo, number)
			if !ok {
				t.Fatal("targets.Default should record the target as closed")
			}
			if outcome != wantOutcome {
				t.Fatalf("Closed outcome = %q, want %q", outcome, wantOutcome)
			}
			target := targets.Target{Repo: repo, Number: number}
			if reason := targets.Default.CheckWrite(target, targets.WritePolicy{}, "comment", repo, number); reason == "" {
				t.Fatal("a write to the now-closed target should be refused")
			}
			t.Cleanup(func() { targets.Default.Reopen(repo, number) })
		})
	}
}
