package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
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

// PutRun keeps a copy, as the real store's file does: a run record read back
// (a restart) shares no maps with the run that wrote it.
func (s *runRecStore) PutRun(r store.WorkflowRun) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var c store.WorkflowRun
	if err := json.Unmarshal(b, &c); err != nil {
		return err
	}
	s.mu.Lock()
	s.recs[r.ID] = c
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
		b, _ := json.Marshal(r)
		var c store.WorkflowRun
		_ = json.Unmarshal(b, &c)
		out = append(out, c)
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

// Two triggers sharing a serialization key: one's run holds it, the other's
// waits (logged, kept in the store), then runs once the first ends.
const serializeCfg = `
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    name: one
    serialize: { key: "pr {{.repo}}#{{.number}}" }
    steps: [{ id: p, uses: eg.post, options: { text: block } }]
  - on: eg.ping
    name: two
    serialize: { key: "pr {{.repo}}#{{.number}}" }
    steps: [{ id: p, uses: eg.post, options: { text: "two {{.msg}}" } }]
`

func serialTrigger(ref string) core.Trigger {
	t := flowTrigger("")
	t.Action = config.Action{FlowRef: ref}
	return t
}

func TestSerializeKeyHoldsAcrossTriggers(t *testing.T) {
	block := setGateBlock(t)
	st := &runRecStore{flowGateStore: newFlowGateStore(), recs: map[string]store.WorkflowRun{}}
	log := &logBuf{}
	eng := capEngine(t, serializeCfg, st, log)
	held := gateCallsWith("block")
	two := gateCallsWith("two hello")

	eng.process(context.Background(), serialTrigger("0:eg.ping"))
	waitCond(t, "one running", func() bool { return gateCallsWith("block") > held })
	eng.process(context.Background(), serialTrigger("1:eg.ping"))
	waitCond(t, "two waiting", func() bool { return log.has("waiting on pr acme/x#1 (held by one)") })
	time.Sleep(30 * time.Millisecond)
	if gateCallsWith("two hello") != two {
		t.Fatal("two ran while one held the key")
	}
	if q := st.queued(); len(q) != 1 {
		t.Fatalf("queued records = %v, want two's", q)
	}
	close(block)
	waitCond(t, "two runs after one", func() bool { return gateCallsWith("two hello") > two })
	waitCond(t, "its queued record removed", func() bool { return len(st.queued()) == 0 })
	waitCond(t, "the key's pool dropped", func() bool {
		eng.runCaps.mu.Lock()
		defer eng.runCaps.mu.Unlock()
		return len(eng.runCaps.serial) == 0
	})
}

// A different target renders a different key: no wait.
func TestSerializeKeyIsPerRenderedKey(t *testing.T) {
	block := setGateBlock(t)
	defer close(block)
	st := &runRecStore{flowGateStore: newFlowGateStore(), recs: map[string]store.WorkflowRun{}}
	eng := capEngine(t, serializeCfg, st, &logBuf{})
	held := gateCallsWith("block")
	two := gateCallsWith("two hello")
	eng.process(context.Background(), serialTrigger("0:eg.ping"))
	waitCond(t, "one running", func() bool { return gateCallsWith("block") > held })
	other := serialTrigger("1:eg.ping")
	other.Target.Number = 2
	eng.process(context.Background(), other)
	waitCond(t, "two runs on another target", func() bool { return gateCallsWith("two hello") > two })
}

// Across a restart: the run that held the key resumes holding it, and the
// run that was waiting on it waits again, then runs.
func TestSerializeKeySurvivesARestart(t *testing.T) {
	block := setGateBlock(t)
	st := &runRecStore{flowGateStore: newFlowGateStore(), recs: map[string]store.WorkflowRun{}}
	eng := capEngine(t, serializeCfg, st, &logBuf{})
	held := gateCallsWith("block")
	two := gateCallsWith("two hello")
	ctx, stop := context.WithCancel(context.Background())
	eng.process(ctx, serialTrigger("0:eg.ping"))
	waitCond(t, "one running", func() bool { return gateCallsWith("block") > held })
	eng.process(ctx, serialTrigger("1:eg.ping"))
	waitCond(t, "two queued", func() bool { return len(st.queued()) == 1 })
	stop() // the daemon goes down: one in flight, two waiting

	log := &logBuf{}
	eng2 := capEngine(t, serializeCfg, st, log)
	eng2.ResumeWorkflows(context.Background())
	waitCond(t, "two waits on the resumed one", func() bool { return log.has("waiting on pr acme/x#1 (held by one)") })
	time.Sleep(30 * time.Millisecond)
	if gateCallsWith("two hello") != two {
		t.Fatal("two ran ahead of the resumed run holding the key")
	}
	close(block)
	waitCond(t, "two runs", func() bool { return gateCallsWith("two hello") == two+1 })
}
