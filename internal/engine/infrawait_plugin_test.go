package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// hungCall is what a plugin transport returns for a call the plugin never
// answered: an error that declares itself an infrastructure failure.
type hungCall struct{ verb string }

func (e hungCall) Error() string {
	return "plugin slowgate: plugin.invoke did not answer within 10m0s (the call was cancelled; the plugin keeps running): " + e.verb
}
func (hungCall) Infrastructure() string { return "plugin slowgate" }

var (
	slowMu    sync.Mutex
	slowHangs int // calls left that hang
	slowCalls int
	slowOnce  sync.Once
)

type slowConnImpl struct{}

func (slowConnImpl) Validate() error          { return nil }
func (slowConnImpl) DeclaredEvents() []string { return nil }
func (slowConnImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (slowConnImpl) Invoke(_ context.Context, verb string, _ map[string]any) (map[string]any, error) {
	slowMu.Lock()
	defer slowMu.Unlock()
	slowCalls++
	if slowHangs > 0 {
		slowHangs--
		return nil, hungCall{verb}
	}
	return map[string]any{"head": "abc123"}, nil
}

func registerSlowConn() {
	slowOnce.Do(func() {
		connector.RegisterType(&connector.TypeDecl{
			Type:  "slowgate",
			Verbs: []connector.VerbDecl{{Name: "snapshot"}},
		}, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
			return slowConnImpl{}, nil
		})
	})
}

const slowCfg = `
connectors:
  eg: { use: enginegate }
  sg: { use: slowgate }
triggers:
  - on: eg.ping
    steps:
      - { id: ack, uses: eg.post, options: { text: "started" } }
      - { id: snapshot, uses: sg.snapshot }
`

// A plugin verb call that never answers (the hung snapshot) is an
// infrastructure failure too: the run waits instead of failing, re-runs the
// step on a backoff — the steps before it stay done — and completes once the
// plugin answers again.
func TestHungPluginCallWaitsThenResumes(t *testing.T) {
	fastInfra(t, time.Hour)
	registerGateConn()
	registerSlowConn()
	slowMu.Lock()
	slowHangs, slowCalls = 2, 0
	slowMu.Unlock()

	var cfg config.Config
	if err := yaml.Unmarshal([]byte(slowCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	logs := &logSink{}
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif, Log: logs.logf})
	eng := New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{}, Notifier: notif, Flow: runner, Connectors: reg, Log: logs.logf})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	eng.baseCtx = ctx

	before := gateCalls()
	eng.process(ctx, infraTrigger())
	waitLong(t, "the run to complete", 30*time.Second, func() bool {
		for _, h := range st.ListHistory(20) {
			if h.Status == "ok" {
				return len(st.PendingRuns()) == 0
			}
		}
		return false
	})
	slowMu.Lock()
	calls := slowCalls
	slowMu.Unlock()
	if calls != 3 {
		t.Fatalf("snapshot called %d times, want 3 (two hangs, then the answer)", calls)
	}
	if got := gateCalls() - before; got != 1 {
		t.Fatalf("the step before the hung one ran %d times, want 1", got)
	}
	if logs.has("workflow failed") {
		t.Fatalf("the run failed instead of waiting: %v", logs.lines)
	}
	var waits int
	for _, h := range st.ListHistory(20) {
		if h.Status == "waiting" {
			waits++
			if !strings.Contains(h.Error, "did not answer within") {
				t.Fatalf("waiting record does not say why: %+v", h)
			}
		}
	}
	if waits != 2 {
		t.Fatalf("want two waiting executions, got %d", waits)
	}
	if !errors.As(error(hungCall{}), new(interface{ Infrastructure() string })) {
		t.Fatal("test marker does not satisfy the contract")
	}
}
