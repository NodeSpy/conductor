package engine

import (
	"context"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The same level-triggered event delivered twice for one head — a webhook,
// then a poll re-deriving the still-pending request — ran two flows side by
// side when the runtime reported no live agent yet (the first run was still
// provisioning, or its steps ran on another runtime): two reviews of one
// head. A run in flight for the same flow, target and head is live for the
// gate; a new head still re-engages beside it.
func TestSameHeadRedeliveryWhileARunIsInFlightDispatchesOnce(t *testing.T) {
	eng, _ := heldEngine(t) // the runtime reports no live agent throughout
	block := setGateBlock(t)
	defer close(block)

	webhook := reviewTrigger("review_requested", "h1", "reviewreq@h1", 0)
	webhook.Context["msg"] = "block" // the run stays in flight until the test ends
	if n := runsOf(eng, webhook); n != 1 {
		t.Fatalf("the webhook's review ran %d times, want 1", n)
	}
	sweep := reviewTrigger("review_requested", "h1", "reviewreq@h1", 0)
	sweep.Context["msg"] = "sweep h1"
	if n := runsOf(eng, sweep); n != 0 {
		t.Fatalf("a re-delivery for the head under review ran %d more flow(s), want 0", n)
	}
	if gateCallsWith("sweep h1") != 0 {
		t.Fatal("the duplicate run posted a second review")
	}

	moved := reviewTrigger("review_requested", "h2", "reviewreq@h2", 0)
	moved.Context["msg"] = "review h2"
	before := gateCallsWith("review h2")
	eng.process(context.Background(), moved)
	waitCond(t, "the new head's review", func() bool { return gateCallsWith("review h2") > before })
}

// The run's end lets the same head dispatch again (the sweep re-derives a
// review still pending after a run that failed).
func TestInFlightGateEndsWithTheRun(t *testing.T) {
	eng, _ := heldEngine(t)
	tr := reviewTrigger("review_requested", "h9", "reviewreq@h9", 0)
	tr.Context["msg"] = "first h9"
	if n := runsOf(eng, tr); n != 1 {
		t.Fatalf("ran %d, want 1", n)
	}
	tr.Context["msg"] = "again h9"
	before := gateCallsWith("again h9")
	eng.process(context.Background(), tr)
	waitCond(t, "the re-derived review", func() bool { return gateCallsWith("again h9") > before })
}

const inflightResumeCfg = `
policy: { concurrency: { max_agents: 1 } }
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    steps:
      - { id: p, uses: eg.post, options: { text: "review {{.head}}" } }
`

// After a restart, a run resumed from its record waits for an agent slot
// before it runs. The source's first poll re-derives its event meanwhile;
// that must not start a second run of it.
func TestResumedRunWaitingForASlotIsInFlight(t *testing.T) {
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(inflightResumeCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	st := tempStore(t)
	tr := reviewTrigger("review_requested", "hr", "reviewreq@hr", 0)
	if err := st.PutRun(flowRunRecord(tr)); err != nil { // the run the last daemon life had in flight
		t.Fatal(err)
	}
	_ = st.RecordAttempt(tr.Key(), "review_requested", "hr")
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	eng := New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{}, Notifier: notif, Flow: runner, Connectors: reg})

	ctx := context.Background()
	if !eng.acquire(ctx) { // every slot busy
		t.Fatal("no slot")
	}
	before := gateCallsWith("review hr")
	eng.ResumeWorkflows(ctx)
	eng.process(ctx, tr) // the first poll after the restart
	time.Sleep(50 * time.Millisecond)
	eng.release()
	waitCond(t, "the resumed run", func() bool { return gateCallsWith("review hr") > before })
	time.Sleep(300 * time.Millisecond)
	if n := gateCallsWith("review hr") - before; n != 1 {
		t.Fatalf("the head was reviewed %d times after the restart, want 1", n)
	}
}
