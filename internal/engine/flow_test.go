package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/connector"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/flow"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

// flowGateStore records the store calls the engine's gates make.
type flowGateStore struct {
	mu       sync.Mutex
	recorded []string
	attempts []string
	audits   []map[string]any
	runs     map[string]bool
	sigs     map[string]string
}

func newFlowGateStore() *flowGateStore {
	return &flowGateStore{runs: map[string]bool{}, sigs: map[string]string{}}
}

func (s *flowGateStore) GC() (int, error) { return 0, nil }
func (s *flowGateStore) Touch(string)     {}
func (s *flowGateStore) Delete(string) error {
	return nil
}
func (s *flowGateStore) LastSignature(key, kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sigs[key+"|"+kind]
}
func (s *flowGateStore) Attempts(string, string, string) int { return 0 }
func (s *flowGateStore) RetryReady(string, string, string, int, time.Duration, int, time.Duration) (bool, time.Duration) {
	return true, 0
}
func (s *flowGateStore) Record(key, kind, sig, head string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, key+"|"+kind)
	s.sigs[key+"|"+kind] = sig
	return nil
}
func (s *flowGateStore) RecordAttempt(key, kind, head string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, key+"|"+kind)
	return nil
}
func (s *flowGateStore) LastCommentID(string, string) int64           { return 0 }
func (s *flowGateStore) AdvanceCommentID(string, string, int64) error { return nil }
func (s *flowGateStore) Audit(e map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, e)
}
func (s *flowGateStore) PutRun(r store.WorkflowRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[r.ID] = true
	return nil
}
func (s *flowGateStore) DeleteRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
	return nil
}
func (s *flowGateStore) PendingRuns() []store.WorkflowRun { return nil }

// fakeFlowDispatcher satisfies the engine Dispatcher (unused by verb-only flows).
type fakeFlowDispatcher struct{}

func (fakeFlowDispatcher) Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error) {
	return dispatch.RunRef{}, nil
}
func (fakeFlowDispatcher) WaitForAgent(context.Context, string, time.Duration) {}
func (fakeFlowDispatcher) HasLiveAgent(context.Context, string, string) bool   { return false }
func (fakeFlowDispatcher) Archive(context.Context, string) error               { return nil }

type fakeNotif struct {
	mu     sync.Mutex
	events []string
}

func (n *fakeNotif) Emit(_ context.Context, event string, _ core.Trigger, msg string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event+":"+msg)
}

// gateConnCalls records verb invocations from the shared test connector.
var (
	gateConnMu    sync.Mutex
	gateConnCalls []map[string]any
	gateConnOnce  sync.Once
)

type gateConnImpl struct{}

func (gateConnImpl) Validate() error          { return nil }
func (gateConnImpl) DeclaredEvents() []string { return nil }
func (gateConnImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (gateConnImpl) Invoke(_ context.Context, verb string, opts map[string]any) (map[string]any, error) {
	gateConnMu.Lock()
	defer gateConnMu.Unlock()
	gateConnCalls = append(gateConnCalls, opts)
	return map[string]any{"ok": true}, nil
}

func registerGateConn() {
	gateConnOnce.Do(func() {
		connector.RegisterType(&connector.TypeDecl{
			Type: "enginegate",
			Events: []connector.EventDecl{{
				Name:    "ping",
				Context: connector.Schema{"msg": {Type: connector.TString}},
			}},
			Verbs: []connector.VerbDecl{{
				Name:    "post",
				Options: connector.Schema{"text": {Type: connector.TString, Required: true}},
			}},
		}, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
			return gateConnImpl{}, nil
		})
	})
}

func gateCalls() int {
	gateConnMu.Lock()
	defer gateConnMu.Unlock()
	return len(gateConnCalls)
}

// buildFlowEngine wires an engine + real flow runner over the test connector.
func buildFlowEngine(t *testing.T, cfgYAML string) (*Engine, *flowGateStore, *fakeNotif, *config.Config) {
	t.Helper()
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	st := newFlowGateStore()
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{
		Cfg: &cfg, Conns: reg, Secrets: secrets.New(),
		Store: st, Notif: notif,
	})
	eng := New(Options{
		Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{},
		Notifier: notif, Flow: runner, Connectors: reg,
	})
	return eng, st, notif, &cfg
}

func flowTrigger(dedup string) core.Trigger {
	return core.Trigger{
		Source: "enginegate", Instance: "eg", Kind: "ping",
		Target:  core.Target{Repo: "acme/x", Number: 1},
		Title:   "ping",
		Dedup:   dedup,
		Context: map[string]any{"msg": "hello", "author": "spammer"},
		Action:  config.Action{FlowRef: "0:eg.ping"},
	}
}

const gateCfg = `
connectors:
  eg: { type: enginegate }
triggers:
  - on: eg.ping
    steps:
      - { id: p, uses: eg.post, options: { text: "{{.msg}}" } }
`

func waitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFlowBranchRunsAndConsumesDedup(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg)
	before := gateCalls()
	eng.process(context.Background(), flowTrigger("d1"))
	waitCond(t, "verb invocation", func() bool { return gateCalls() > before })
	st.mu.Lock()
	recorded := len(st.recorded)
	st.mu.Unlock()
	if recorded != 1 {
		t.Fatalf("dedup records: %d, want 1", recorded)
	}
	// The same dedup signature is suppressed on re-delivery.
	calls := gateCalls()
	eng.process(context.Background(), flowTrigger("d1"))
	time.Sleep(50 * time.Millisecond)
	if gateCalls() != calls {
		t.Fatal("duplicate delivery re-ran the flow")
	}
}

func TestFlowPolicyIgnoreUsers(t *testing.T) {
	cfg := strings.Replace(gateCfg, "type: enginegate }", "type: enginegate, policy: { ignore: { users: [spammer] } } }", 1)
	eng, st, _, _ := buildFlowEngine(t, cfg)
	before := gateCalls()
	eng.process(context.Background(), flowTrigger("d2"))
	time.Sleep(50 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("ignored author still ran the flow")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.recorded) != 0 {
		t.Fatal("ignored trigger consumed dedup state")
	}
}

func TestFlowTriggerDisabled(t *testing.T) {
	// The supported per-trigger switch is the trigger's own `enabled: false`
	// (policy has no enabled key — the global kill switch is `conductor pause`).
	cfg := strings.Replace(gateCfg, "steps:", "enabled: false\n    steps:", 1)
	eng, _, _, _ := buildFlowEngine(t, cfg)
	before := gateCalls()
	eng.process(context.Background(), flowTrigger("d3"))
	time.Sleep(50 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("disabled trigger ran")
	}
}

func TestFlowQuietHoursDrop(t *testing.T) {
	// A 00:00–23:59 window with hold: false drops everything, any time of day.
	cfg := strings.Replace(gateCfg, "steps:",
		`policy: { quiet_hours: { from: "00:00", to: "23:59", hold: false } }
    steps:`, 1)
	eng, _, _, _ := buildFlowEngine(t, cfg)
	before := gateCalls()
	eng.process(context.Background(), flowTrigger("d4"))
	time.Sleep(50 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("quiet-hours drop still ran the flow")
	}
}

func TestFlowStaleRefDropped(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg)
	tr := flowTrigger("d5")
	tr.Action = config.Action{FlowRef: "7:eg.gone"}
	before := gateCalls()
	eng.process(context.Background(), tr)
	time.Sleep(50 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("stale flow ref ran something")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.recorded) != 0 {
		t.Fatal("stale ref consumed dedup")
	}
}

func TestFlowGroupBatches(t *testing.T) {
	cfg := strings.Replace(gateCfg, "steps:",
		`group: { key: "{{.repo}}", window: 30ms }
    steps:`, 1)
	cfg = strings.Replace(cfg, `text: "{{.msg}}"`, `text: "batch {{.group.count}}"`, 1)
	eng, _, _, _ := buildFlowEngine(t, cfg)
	before := gateCalls()
	eng.process(context.Background(), flowTrigger("g1"))
	eng.process(context.Background(), flowTrigger("g2"))
	waitCond(t, "batched run", func() bool { return gateCalls() > before })
	time.Sleep(100 * time.Millisecond) // no second run
	if got := gateCalls() - before; got != 1 {
		t.Fatalf("grouped events ran %d flows, want 1", got)
	}
	gateConnMu.Lock()
	last := gateConnCalls[len(gateConnCalls)-1]
	gateConnMu.Unlock()
	if last["text"] != "batch 2" {
		t.Fatalf("batch context wrong: %v", last)
	}
}
