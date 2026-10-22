package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// liveSwitch is a fakeFlowDispatcher whose HasLiveAgent answer the test flips.
type liveSwitch struct {
	fakeFlowDispatcher
	live *atomic.Bool
}

func (d liveSwitch) HasLiveAgent(context.Context, string, string) bool { return d.live.Load() }

func heldEngine(t *testing.T) (*Engine, *atomic.Bool) {
	t.Helper()
	registerGateConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(gateCfg), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	st := &markStore{flowGateStore: newFlowGateStore(), marks: map[string]int64{}}
	notif := &fakeNotif{}
	live := &atomic.Bool{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	eng := New(Options{Config: &cfg, Store: st, Dispatch: liveSwitch{live: live}, Notifier: notif, Flow: runner, Connectors: reg})
	return eng, live
}

// runs processes tr and reports how many verb calls its flow made.
func runsOf(eng *Engine, tr core.Trigger) int {
	before := gateCalls()
	eng.process(context.Background(), tr)
	time.Sleep(150 * time.Millisecond)
	return gateCalls() - before
}

// redelivered drains what the engine re-emitted onto its queue.
func redelivered(eng *Engine) []core.Trigger {
	var out []core.Trigger
	for {
		select {
		case tr := <-eng.ch:
			out = append(out, tr)
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

// A review submitted while a run is already working on the PR was skipped by
// the live gate and never came back: the run ended, a later review dispatched
// and raised the mark past it, and every redelivery or poll of it then fell
// at or below the mark. A cursor-carrying event the live gate skips is now
// held, and re-delivered — through every gate again — when a run on its
// target ends.
func TestLiveGatedReviewIsRedeliveredWhenTheRunEnds(t *testing.T) {
	eng, live := heldEngine(t)

	if n := runsOf(eng, reviewTrigger("changes_requested", "h1", "review:1@h1", 100)); n != 1 {
		t.Fatalf("the first review ran %d times, want 1", n)
	}
	live.Store(true) // its agent is still working
	if n := runsOf(eng, reviewTrigger("changes_requested", "h1", "review:2@h1", 200)); n != 0 {
		t.Fatalf("a review that landed mid-run ran %d times while the agent was live, want 0", n)
	}
	if got := redelivered(eng); len(got) != 0 {
		t.Fatalf("nothing may be re-delivered while the agent is still live, got %d", len(got))
	}

	// The agent finishes; a run on the PR ends (any flow on the target).
	live.Store(false)
	if n := runsOf(eng, flowTrigger("ping:end")); n != 1 {
		t.Fatalf("the ending run ran %d times, want 1", n)
	}
	got := redelivered(eng)
	if len(got) != 1 || got[0].Dedup != "review:2@h1" {
		t.Fatalf("the held review must be re-delivered once its run ends, got %v", dedups(got))
	}
	if n := runsOf(eng, got[0]); n != 1 {
		t.Fatalf("the re-delivered review ran %d times, want 1", n)
	}
	// Re-delivered again (a stale duplicate), it is at the mark: dropped.
	if n := runsOf(eng, got[0]); n != 0 {
		t.Fatalf("a handled review ran again (%d)", n)
	}
}

// Several reviews held during one run: the one with the highest cursor is
// kept (with the source folding every unaddressed review into each event,
// it carries the others too).
func TestHeldEventKeepsTheHighestCursor(t *testing.T) {
	eng, live := heldEngine(t)
	live.Store(true)
	runsOf(eng, reviewTrigger("changes_requested", "h1", "review:3@h1", 300))
	runsOf(eng, reviewTrigger("changes_requested", "h1", "review:2@h1", 200)) // a late, older delivery
	live.Store(false)
	runsOf(eng, flowTrigger("ping:end2"))
	if got := redelivered(eng); len(got) != 1 || got[0].Dedup != "review:3@h1" {
		t.Fatalf("want only the highest-cursor held event, got %v", dedups(got))
	}
}

// The run may end between the live check and the hold; the hold looks again
// and re-delivers at once rather than waiting for a run that already ended.
func TestHeldEventRedeliveredWhenTheRunAlreadyEnded(t *testing.T) {
	eng, live := heldEngine(t)
	live.Store(true)
	tr := reviewTrigger("changes_requested", "h1", "review:4@h1", 400)
	act := tr.Action.(config.Action)
	eng.held.put("x", "y", core.Trigger{}) // unrelated entry stays put
	live.Store(false)
	if !eng.holdLevel(context.Background(), tr, act, tr.Key(), "changes_requested") {
		t.Fatal("a cursor-carrying event must be held")
	}
	if got := redelivered(eng); len(got) != 1 || got[0].Dedup != "review:4@h1" {
		t.Fatalf("no live agent left: the held event must be re-delivered at once, got %v", dedups(got))
	}
	if left := eng.held.take("x"); len(left) != 1 {
		t.Fatal("re-delivery must take only its own target's events")
	}
}

// An event with no cursor only re-reads a condition (a review request, a
// conflict); replaying it later would act on a stale reading, so it is not
// held. And the target closing drops what it held.
func TestHeldOnlyCursorEventsAndDroppedOnClose(t *testing.T) {
	eng, live := heldEngine(t)
	live.Store(true)
	runsOf(eng, reviewTrigger("review_requested", "h1", "reviewreq@h1", 0))
	runsOf(eng, reviewTrigger("changes_requested", "h1", "review:5@h1", 500))
	runsOf(eng, core.Trigger{Source: "enginegate", Instance: "eg", Kind: "_closed",
		Target: core.Target{Repo: "acme/x", PR: 1, Number: 1}})
	live.Store(false)
	runsOf(eng, flowTrigger("ping:end3"))
	if got := redelivered(eng); len(got) != 0 {
		t.Fatalf("nothing may be re-delivered (cursor-less, or its target closed), got %v", dedups(got))
	}
}

// A parked hand-off is a live agent too, and no flow run ends when it is
// released (done, idle timeout, a watch rule): its release re-delivers what
// the live gate held on its target.
func TestHeldEventRedeliveredWhenAHandoffIsReleased(t *testing.T) {
	eng, live := heldEngine(t)
	live.Store(true)
	tr := reviewTrigger("changes_requested", "h1", "review:6@h1", 600)
	runsOf(eng, tr)
	if got := redelivered(eng); len(got) != 0 {
		t.Fatalf("held while the hand-off is parked, got %v", dedups(got))
	}
	live.Store(false)
	eng.registerLiveHandoff("ag-ho", func() {}, tr.Key(), "review_requested", "review")
	eng.releaseHandoff(context.Background(), "ag-ho", tr.Key(), "review", func() {}, "agent signalled done")
	if got := redelivered(eng); len(got) != 1 || got[0].Dedup != "review:6@h1" {
		t.Fatalf("the hand-off's release must re-deliver the held review, got %v", dedups(got))
	}
}

func dedups(ts []core.Trigger) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Dedup)
	}
	return out
}
