package engine

import (
	"context"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// TestParkFiresFailHooks: when the engine PARKS a (PR, kind, head) — the
// attempts ran out; nothing more happens until new commits — the trigger's
// workflow-level fail hooks fire (kind "parked"), so whatever a fail hook
// posts (a 😕, a failure status) says so instead of the event looking picked
// up and abandoned. Only the parking pass fires them.
func TestParkFiresFailHooks(t *testing.T) {
	e, st, _, _ := buildFlowEngine(t, `
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    hooks:
      - { at: fail, uses: eg.post, options: { text: "parked kind={{.hook.failure.kind}} start={{.run.start_sha}}" } }
    steps:
      - { id: p, uses: eg.post, options: { text: x } }
`)
	gateConnMu.Lock()
	gateConnCalls = nil
	gateConnMu.Unlock()
	tr := core.Trigger{Source: "enginegate", Instance: "eg", Kind: "ping",
		Target: core.Target{Repo: "acme/x", Number: 1, HeadSHA: "h"}, Dedup: "d1",
		Action: config.Action{FlowRef: "0:eg.ping", MaxAttemptsPerHead: 1}}
	st.perHead = map[string]int{tr.Key() + "|" + dedupKindOf(tr) + "|h": 2} // at the park ceiling
	e.process(context.Background(), tr)
	waitCond(t, "parked fail hook", func() bool { return gateCalls() == 1 })
	gateConnMu.Lock()
	got := gateConnCalls[0]["text"]
	gateConnMu.Unlock()
	if got != "parked kind=parked start=h" {
		t.Fatalf("parked hook = %v", got)
	}
	// Already parked: silent.
	tr.Dedup = "d2"
	e.process(context.Background(), tr)
	time.Sleep(50 * time.Millisecond) // the hooks run off the engine loop
	if n := gateCalls(); n != 1 {
		t.Fatalf("a parked tuple fired its hooks again (%d calls)", n)
	}
}
