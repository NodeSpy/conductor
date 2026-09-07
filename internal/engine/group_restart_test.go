package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildFlowEngineOn is buildFlowEngine over a caller-provided store, so a
// test can "restart" the engine while the persisted dedup state survives.
func buildFlowEngineOn(t *testing.T, cfgYAML string, st *flowGateStore) *Engine {
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
		Notifier: notif, Flow: runner, Connectors: reg})
}

const groupRestartCfg = `
connectors:
  eg: { type: enginegate }
triggers:
  - on: eg.ping
    group: { key: "{{.repo}}", window: 40ms }
    steps:
      - { id: p, uses: eg.post, options: { text: "batch {{.group.count}}" } }
`

// Regression: grouped events used to consume dedup state at ACCEPT time and
// then buffer in the in-memory Grouper — a restart dropped the batch while
// the recorded signature suppressed redelivery, silently losing the events.
// Dedup now records at flush time: an event buffered when the process dies
// is redeliverable to the next engine, and only a FIRED batch suppresses.
func TestGroupedEventsSurviveRestart(t *testing.T) {
	st := newFlowGateStore()
	// Engine 1 gets an hour-long window: its in-memory batch never fires
	// inside this test, standing in for a process killed mid-window.
	eng1 := buildFlowEngineOn(t, strings.Replace(groupRestartCfg, "window: 40ms", "window: 1h", 1), st)

	// Buffer one event on engine 1 and kill the engine before the window
	// fires (nothing to stop — we simply never let it flush by not waiting).
	eng1.process(context.Background(), flowTrigger("evt-1"))
	st.mu.Lock()
	recorded, sigs := len(st.recorded), len(st.sigs)
	st.mu.Unlock()
	if recorded != 0 || sigs != 0 {
		t.Fatalf("dedup was consumed at accept time (recorded=%d sigs=%d) — a restart here loses the batch", recorded, sigs)
	}

	// "Restart": a fresh engine over the SAME store. The in-memory buffer of
	// eng1 is gone; the source redelivers the event.
	before := gateCalls()
	eng2 := buildFlowEngineOn(t, groupRestartCfg, st)
	eng2.process(context.Background(), flowTrigger("evt-1"))
	waitCond(t, "redelivered event fires after restart", func() bool { return gateCalls() > before })

	// The flush recorded the dedup signature, so a THIRD delivery is now
	// suppressed.
	st.mu.Lock()
	recorded = len(st.recorded)
	st.mu.Unlock()
	if recorded != 1 {
		t.Fatalf("flush-time dedup records: %d, want 1", recorded)
	}
	calls := gateCalls()
	eng2.process(context.Background(), flowTrigger("evt-1"))
	time.Sleep(120 * time.Millisecond)
	if gateCalls() != calls {
		t.Fatal("post-flush redelivery re-ran the batch")
	}
}

// Redelivery of a still-buffered event must not double it inside the batch:
// the flush drops intra-batch duplicates by signature.
func TestGroupedRedeliveryDedupesWithinBatch(t *testing.T) {
	st := newFlowGateStore()
	eng := buildFlowEngineOn(t, groupRestartCfg, st)
	before := gateCalls()
	eng.process(context.Background(), flowTrigger("dup-1"))
	eng.process(context.Background(), flowTrigger("dup-1")) // redelivery in-window
	eng.process(context.Background(), flowTrigger("dup-2"))
	waitCond(t, "batched run", func() bool { return gateCalls() > before })
	gateConnMu.Lock()
	last := gateConnCalls[len(gateConnCalls)-1]
	gateConnMu.Unlock()
	if last["text"] != "batch 2" {
		t.Fatalf("batch should hold 2 unique events, got: %v", last)
	}
	if !strings.HasPrefix(last["text"].(string), "batch") {
		t.Fatalf("unexpected call: %v", last)
	}
}

// Regression: a typo'd group key renders "" under missingkey=zero, which
// used to collapse EVERY event — all repos, all entities — into one shared
// batch. An empty rendered key now degrades to per-event batching.
func TestGroupEmptyKeyBatchesPerEvent(t *testing.T) {
	st := newFlowGateStore()
	eng := buildFlowEngineOn(t, strings.Replace(groupRestartCfg,
		`key: "{{.repo}}"`, `key: "{{.nosuchfield}}"`, 1), st)
	before := gateCalls()
	a := flowTrigger("e-a")
	b := flowTrigger("e-b")
	b.Target = core.Target{Repo: "acme/other", Number: 9}
	eng.process(context.Background(), a)
	eng.process(context.Background(), b)
	waitCond(t, "both per-event runs", func() bool { return gateCalls()-before >= 2 })
	if got := gateCalls() - before; got != 2 {
		t.Fatalf("empty group key ran %d flows, want 2 (per-event)", got)
	}
	// Each run saw a batch of exactly one event, not a cross-entity merge.
	gateConnMu.Lock()
	texts := []string{}
	for _, c := range gateConnCalls[len(gateConnCalls)-2:] {
		texts = append(texts, c["text"].(string))
	}
	gateConnMu.Unlock()
	for _, txt := range texts {
		if txt != "batch 1" {
			t.Fatalf("cross-entity collapse: %v", texts)
		}
	}
}
