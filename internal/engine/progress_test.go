package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// A connector type with a progress face, recording what the engine asks of it.
var (
	engProgOnce sync.Once
	engProgMu   sync.Mutex
	engProgLog  []string
)

type engProgImpl struct{ gateConnImpl }

func (engProgImpl) StartProgress(context.Context, connector.ProgressRun) connector.Progress {
	engProgMu.Lock()
	engProgLog = append(engProgLog, "start")
	engProgMu.Unlock()
	return engProgHandle{}
}

type engProgHandle struct{}

func (engProgHandle) Finish(_ context.Context, o connector.RunOutcome) {
	engProgMu.Lock()
	engProgLog = append(engProgLog, "finish:"+o.Result+":"+o.Reason)
	engProgMu.Unlock()
}

func engProg() []string {
	engProgMu.Lock()
	defer engProgMu.Unlock()
	return append([]string(nil), engProgLog...)
}

// TestParkShowsFailedProgress: when the engine PARKS a (PR, kind, head) — the
// attempts ran out, nothing more will happen until new commits — the event's
// subject is told so (a failed outcome), instead of looking picked up and
// abandoned. Only the parking pass reports; later passes over a parked tuple
// stay silent.
func TestParkShowsFailedProgress(t *testing.T) {
	engProgOnce.Do(func() {
		connector.RegisterType(&connector.TypeDecl{
			Type:   "engineprog",
			Events: []connector.EventDecl{{Name: "ping", Context: connector.Schema{"msg": {Type: connector.TString}}}},
			Verbs: []connector.VerbDecl{{Name: "post",
				Options: connector.Schema{"text": {Type: connector.TString, Required: true}}}},
		}, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
			return engProgImpl{}, nil
		})
	})
	e, st, _, _ := buildFlowEngine(t, `
connectors:
  ep: { use: engineprog }
triggers:
  - on: ep.ping
    steps:
      - { id: p, uses: ep.post, options: { text: x } }
`)
	tr := core.Trigger{Source: "engineprog", Instance: "ep", Kind: "ping",
		Target: core.Target{Repo: "acme/x", Number: 1, HeadSHA: "h"}, Dedup: "d1",
		Action: config.Action{FlowRef: "0:ep.ping", MaxAttemptsPerHead: 1}}
	st.perHead = map[string]int{tr.Key() + "|" + dedupKindOf(tr) + "|h": 2} // at the park ceiling
	e.process(context.Background(), tr)
	waitCond(t, "parked progress", func() bool { return len(engProg()) == 2 })
	got := engProg()
	if got[0] != "start" || got[1] != "finish:failed:parked after repeated tries — needs a human or new commits" {
		t.Fatalf("park progress = %v", got)
	}
	// Already parked: no second report.
	tr.Dedup = "d2"
	e.process(context.Background(), tr)
	time.Sleep(50 * time.Millisecond) // the report runs off the engine loop
	if n := len(engProg()); n != 2 {
		t.Fatalf("a parked tuple reported again: %v", engProg())
	}
}
