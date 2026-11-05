package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
	"gopkg.in/yaml.v3"
)

// runRecStore keeps the run records a restart resumes from.
type runRecStore struct {
	*flowGateStore
	mu   sync.Mutex
	recs map[string]store.WorkflowRun
}

func (s *runRecStore) PutRun(r store.WorkflowRun) error {
	s.mu.Lock()
	s.recs[r.ID] = r
	s.mu.Unlock()
	return s.flowGateStore.PutRun(r)
}

func (s *runRecStore) DeleteRun(id string) error {
	s.mu.Lock()
	delete(s.recs, id)
	s.mu.Unlock()
	return s.flowGateStore.DeleteRun(id)
}

func (s *runRecStore) PendingRuns() []store.WorkflowRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.WorkflowRun
	for _, r := range s.recs {
		out = append(out, r)
	}
	return out
}

func (s *runRecStore) queued() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id := range s.recs {
		if strings.HasPrefix(id, queuedRunPrefix) {
			ids = append(ids, id)
		}
	}
	return ids
}

type logBuf struct {
	mu  sync.Mutex
	out []string
}

func (l *logBuf) logf(f string, a ...any) {
	l.mu.Lock()
	l.out = append(l.out, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}

func (l *logBuf) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range l.out {
		if strings.Contains(o, s) {
			return true
		}
	}
	return false
}

func capEngine(t *testing.T, cfgYAML string, st *runRecStore, log *logBuf) *Engine {
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
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	return New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{},
		Notifier: notif, Flow: runner, Connectors: reg, Log: log.logf})
}

const runCapCfg = `
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    policy: { concurrency: { max_runs: 1 } }
    steps:
      - { id: p, uses: eg.post, options: { text: "{{.msg}}" } }
`

// holdTriggerSlot takes the trigger's only run slot, as a long run would.
func holdTriggerSlot(t *testing.T, eng *Engine) *slots {
	t.Helper()
	spec, _, ok := eng.flow.SpecFor("0:eg.ping")
	if !ok {
		t.Fatal("no spec")
	}
	caps := eng.runCapsFor("0:eg.ping", spec)
	if len(caps) != 1 || !caps[0].slots.acquire(context.Background(), prioNormal) {
		t.Fatalf("caps = %v", caps)
	}
	return caps[0].slots
}

// With the trigger's max_runs taken, a new run queues (logged), is kept in the
// store while it waits, and runs once the slot frees; its queued record goes.
func TestRunCapQueuesUntilASlotFrees(t *testing.T) {
	st := &runRecStore{flowGateStore: newFlowGateStore(), recs: map[string]store.WorkflowRun{}}
	log := &logBuf{}
	eng := capEngine(t, runCapCfg, st, log)
	held := holdTriggerSlot(t, eng)
	before := gateCalls()

	eng.process(context.Background(), flowTrigger("q1"))
	waitCond(t, "the wait logged", func() bool { return log.has("waiting for a run slot (1/1 running, trigger 0:eg.ping)") })
	time.Sleep(30 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("ran past max_runs")
	}
	if q := st.queued(); len(q) != 1 {
		t.Fatalf("queued records = %v, want one", q)
	}
	held.release()
	waitCond(t, "the queued run", func() bool { return gateCalls() > before })
	waitCond(t, "its queued record removed", func() bool { return len(st.queued()) == 0 })
}

// A run still queued when the daemon stops is re-queued at the next start,
// and runs.
func TestRunCapQueuedRunSurvivesARestart(t *testing.T) {
	st := &runRecStore{flowGateStore: newFlowGateStore(), recs: map[string]store.WorkflowRun{}}
	log := &logBuf{}
	eng := capEngine(t, runCapCfg, st, log)
	holdTriggerSlot(t, eng)
	ctx, stop := context.WithCancel(context.Background())
	eng.process(ctx, flowTrigger("r1"))
	waitCond(t, "queued", func() bool { return len(st.queued()) == 1 })
	stop() // the daemon goes down with the run still queued
	time.Sleep(30 * time.Millisecond)
	if q := st.queued(); len(q) != 1 {
		t.Fatalf("queued record after shutdown = %v, want it kept", q)
	}

	before := gateCalls()
	eng2 := capEngine(t, runCapCfg, st, log)
	eng2.ResumeWorkflows(context.Background())
	waitCond(t, "re-queued run", func() bool { return gateCalls() > before })
	if !log.has("re-queued after a restart") {
		t.Error("re-queue not logged")
	}
	waitCond(t, "its queued record removed", func() bool { return len(st.queued()) == 0 })
}

// Caps are taken narrowest first, and a connector's cap is one pool for all
// its triggers.
func TestRunCapsForScopes(t *testing.T) {
	cfg := `
policy: { concurrency: { max_runs: 5 } }
connectors:
  eg: { use: enginegate, policy: { concurrency: { max_runs: 2 } } }
triggers:
  - on: eg.ping
    name: one
    policy: { concurrency: { max_runs: 1 } }
    steps: [{ id: p, uses: eg.post, options: { text: x } }]
  - on: eg.ping
    name: two
    steps: [{ id: p, uses: eg.post, options: { text: x } }]
`
	st := &runRecStore{flowGateStore: newFlowGateStore(), recs: map[string]store.WorkflowRun{}}
	eng := capEngine(t, cfg, st, &logBuf{})
	s1, _, _ := eng.flow.SpecFor("0:eg.ping")
	s2, _, _ := eng.flow.SpecFor("1:eg.ping")
	c1, c2 := eng.runCapsFor("0:eg.ping", s1), eng.runCapsFor("1:eg.ping", s2)
	var scopes []string
	for _, c := range c1 {
		scopes = append(scopes, c.scope)
	}
	if strings.Join(scopes, ",") != "trigger one,connector eg,global" {
		t.Fatalf("scopes = %v", scopes)
	}
	if len(c2) != 2 || c2[0].slots != c1[1].slots || c2[1].slots != c1[2].slots {
		t.Fatal("two triggers of one connector do not share its pool (or the global one)")
	}
	if _, n := c1[0].slots.usage(); n != 1 {
		t.Errorf("trigger cap = %d", n)
	}
}
