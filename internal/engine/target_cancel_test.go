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
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/internal/targets"
)

// fakeCancelDispatcher is the engine Dispatcher, and so the built-in paseo
// runner of the registry engine.New builds (Options.Controllers is nil). It
// implements StopTarget the way *dispatch.Dispatcher does — archive the
// target's PR-fixer agents — so the test drives the one stop path end to end:
// process(_closed) → stopFixers → every runner's StopTarget.
type fakeCancelDispatcher struct {
	mu      sync.Mutex
	agents  map[string]map[string][]string // pr key -> kind -> agent ids
	stopped []string
}

func (d *fakeCancelDispatcher) Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error) {
	return dispatch.RunRef{}, nil
}
func (d *fakeCancelDispatcher) WaitForAgent(context.Context, string, time.Duration) {}
func (d *fakeCancelDispatcher) HasLiveAgent(context.Context, string, string) bool   { return false }
func (d *fakeCancelDispatcher) Archive(context.Context, string) error               { return nil }
func (d *fakeCancelDispatcher) AgentForDispatch(string) string                      { return "" }
func (d *fakeCancelDispatcher) DispatchInFlight(string) bool                        { return false }
func (d *fakeCancelDispatcher) DeliverOutput(string, any) (bool, error)             { return false, nil }
func (d *fakeCancelDispatcher) StopTarget(_ context.Context, key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for kind, ids := range d.agents[key] {
		if core.BranchFixKind(kind) {
			d.stopped = append(d.stopped, ids...)
			n += len(ids)
		}
	}
	return n
}
func (d *fakeCancelDispatcher) stoppedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.stopped...)
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
// incident end to end through the single stop path: a trusted `_closed`
// trigger (merged and closed-unmerged) must stop the target's running fixer
// — and only its fixer, not a review agent on the same PR — leave a
// fixers_stopped audit row carrying the reason, notify, and (because it also
// marks the target closed in internal/targets) refuse a later write to it.
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
			d := &fakeCancelDispatcher{agents: map[string]map[string][]string{
				prKey:            {"merge_conflict": {"fixer-1"}, "review_requested": {"review-1"}},
				"acme/other#100": {"merge_conflict": {"elsewhere"}},
			}}
			eng, st, notif := buildCancelEngine(t, gateCfg2(), d)
			st.RecordEngagement(prKey, store.Engagement{Key: "fixer"})

			eng.process(context.Background(), closedTrigger(repo, number, merged, nil))

			if got := d.stoppedIDs(); len(got) != 1 || got[0] != "fixer-1" {
				t.Fatalf("stopped = %v, want only [fixer-1] (not the reviewer, not another PR's fixer)", got)
			}

			st.mu.Lock()
			var row map[string]any
			for _, a := range st.audits {
				if a["event"] == "fixers_stopped" {
					row = a
				}
			}
			st.mu.Unlock()
			if row == nil {
				t.Fatalf("expected a fixers_stopped audit row, got: %+v", st.audits)
			}
			if row["reason"] != wantReason || row["count"] != 1 {
				t.Fatalf("fixers_stopped reason/count = %v/%v, want %q/1", row["reason"], row["count"], wantReason)
			}
			if row["repo"] != repo || row["number"] != number {
				t.Fatalf("fixers_stopped repo/number = %v/%v, want %s/%d", row["repo"], row["number"], repo, number)
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

			// The close is recorded as the connector-neutral "target closed"
			// signal the gh/git profiles and the github connector's scopes
			// refuse writes on (tested there).
			outcome, ok := targets.Default.Closed(repo, number)
			if !ok {
				t.Fatal("targets.Default should record the target as closed")
			}
			if outcome != wantOutcome {
				t.Fatalf("Closed outcome = %q, want %q", outcome, wantOutcome)
			}
			t.Cleanup(func() { targets.Default.Reopen(repo, number) })
		})
	}
}
