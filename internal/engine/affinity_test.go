package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// sendingDispatcher is a fakeDispatcher that also accepts session follow-ups
// (`paseo send`) — making the engine's built-in paseo controller
// session-persistent, the production shape for affinity.
type sendingDispatcher struct {
	fakeDispatcher
	mu       sync.Mutex
	sent     []string
	block    chan struct{} // when set, Send waits on it (turn in flight)
	inFlight int
}

func (d *sendingDispatcher) Send(_ context.Context, id, prompt string) error {
	d.mu.Lock()
	d.inFlight++
	block := d.block
	d.mu.Unlock()
	if block != nil {
		<-block
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.inFlight--
	d.sent = append(d.sent, id+"\x00"+prompt)
	return nil
}

func (d *sendingDispatcher) sends() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.sent...)
}

// waitSends polls for n delivered follow-ups (delivery is asynchronous — the
// engine loop must never block on a turn).
func waitSends(t *testing.T, d *sendingDispatcher, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(d.sends()) == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d follow-ups (have %d)", n, len(d.sends()))
}

// affinityEngine builds an engine whose registry + affinity share one
// dispatcher, mirroring main.go's wiring.
func affinityEngine(t *testing.T, cfg *config.Config, d *sendingDispatcher) *Engine {
	t.Helper()
	reg := controller.NewRegistry(cfg.MergedControllers(), cfg.DefaultRuntimeName(), d, d)
	aff := controller.NewAffinity(reg, nil, cfg, nil, nil, nil)
	e := New(Options{
		Config: cfg, Store: tempStore(t), Dispatch: d, Controllers: reg, Affinity: aff,
		Notifier:  &fakeNotifier{},
		UserToken: func() (string, error) { return "utok", nil },
	})
	return e
}

func affinityCfg() *config.Config {
	cfg := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "pr-agent", Session: &config.SessionSpec{
			Key:   "{{.repo}}#{{.number}}",
			EndOn: []string{"i.pr_closed"},
		}},
		{ID: "fresh-agent"},
	}}}}
	cfg.Control.Enabled = ptrBool(true)
	return cfg
}

// TestEngineAffinityRoutesFollowups: through the engine's real dispatch path,
// the first event for a key spawns and later events — different kinds —
// become follow-ups to the same agent; a non-session profile stays
// fresh-per-event.
func TestEngineAffinityRoutesFollowups(t *testing.T) {
	cfg := affinityCfg()
	d := &sendingDispatcher{fakeDispatcher: fakeDispatcher{ref: dispatch.RunRef{AgentID: "agent-1"}}}
	e := affinityEngine(t, cfg, d)

	act := func(agent string) config.Action {
		return config.Action{Type: "agent", Agent: agent, Prompt: "work {{.kind}}"}
	}
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h1", "s1", act("w/pr-agent")))
	if len(d.reqs) != 1 {
		t.Fatalf("first event must spawn: %d dispatches", len(d.reqs))
	}
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 1, "h2", "s2", act("w/pr-agent")))
	e.process(context.Background(), agentTrigger("failing_checks", "a/w", 1, "h3", "s3", act("w/pr-agent")))
	if len(d.reqs) != 1 {
		t.Fatalf("same-key events must not spawn again: %d dispatches", len(d.reqs))
	}
	waitSends(t, d, 2)
	sent := d.sends()
	if !strings.HasPrefix(sent[0], "agent-1\x00") || !strings.Contains(sent[0], "work merge_conflict") {
		t.Fatalf("follow-ups: %v", sent)
	}

	// A different PR spawns its own session.
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 2, "h4", "s4", act("w/pr-agent")))
	if len(d.reqs) != 2 {
		t.Fatalf("a new key must spawn: %d dispatches", len(d.reqs))
	}

	// A profile without session: dispatches fresh every time.
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 3, "h5", "s5", act("w/fresh-agent")))
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h6", "s6", act("w/fresh-agent")))
	if len(d.reqs) != 4 {
		t.Fatalf("non-session profile must stay fresh-per-event: %d dispatches", len(d.reqs))
	}
}

// TestEngineAffinityEndOnEvent: an end_on lifecycle event arriving at the
// engine evicts the keyed session before any gate; the next event respawns.
func TestEngineAffinityEndOnEvent(t *testing.T) {
	cfg := affinityCfg()
	d := &sendingDispatcher{fakeDispatcher: fakeDispatcher{ref: dispatch.RunRef{AgentID: "agent-1"}}}
	e := affinityEngine(t, cfg, d)
	act := config.Action{Type: "agent", Agent: "w/pr-agent", Prompt: "work"}

	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h1", "s1", act))
	if !e.affinityOwns("agent-1") {
		t.Fatal("session must be bound")
	}
	// pr_closed arrives (agentTrigger uses instance "i" — matched by
	// end_on "i.pr_closed"). It carries no action the engine would run;
	// eviction happens at observe time.
	closed := core.Trigger{Source: "github", Instance: "i", Kind: "pr_closed",
		TargetTrusted: true,
		Target:        core.Target{Repo: "a/w", PR: 1, Number: 1}}
	e.process(context.Background(), closed)
	if e.affinityOwns("agent-1") {
		t.Fatal("end_on event must evict the session")
	}
	d.ref = dispatch.RunRef{AgentID: "agent-2"}
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h2", "s2", act))
	if len(d.reqs) != 2 {
		t.Fatalf("post-eviction event must spawn fresh: %d", len(d.reqs))
	}
}

// REGRESSION (head-of-line blocking): the engine's Run loop is a single
// goroutine and used to deliver session follow-ups INLINE — one in-flight
// turn stalled every other trigger (and Emit's buffer then dropped events).
// A blocked follow-up must not delay a different-key trigger; same-key
// events still serialize (one prompt in flight per key).
func TestEngineAffinityFollowupDoesNotBlockLoop(t *testing.T) {
	cfg := affinityCfg()
	d := &sendingDispatcher{fakeDispatcher: fakeDispatcher{ref: dispatch.RunRef{AgentID: "agent-1"}}}
	e := affinityEngine(t, cfg, d)
	act := config.Action{Type: "agent", Agent: "w/pr-agent", Prompt: "work {{.kind}}"}

	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h1", "s1", act)) // bind PR 1

	// Wedge the sender: the next follow-up's turn stays in flight.
	gate := make(chan struct{})
	d.mu.Lock()
	d.block = gate
	d.mu.Unlock()
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 1, "h2", "s2", act))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		inFlight := d.inFlight
		d.mu.Unlock()
		if inFlight > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A DIFFERENT key must dispatch promptly while that turn is in flight.
	d.ref = dispatch.RunRef{AgentID: "agent-2"}
	start := time.Now()
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 2, "h3", "s3", act))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("different-key trigger was blocked behind an in-flight turn: %s", elapsed)
	}
	if len(d.reqs) != 2 {
		t.Fatalf("PR 2 must spawn its own session while PR 1's turn is in flight: %d dispatches", len(d.reqs))
	}

	// A SAME-key event also returns promptly (enqueued), and serializes: it
	// must not enter Send until the wedged turn ends.
	start = time.Now()
	e.process(context.Background(), agentTrigger("failing_checks", "a/w", 1, "h4", "s4", act))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("same-key trigger blocked the engine loop: %s", elapsed)
	}
	d.mu.Lock()
	inFlight := d.inFlight
	d.mu.Unlock()
	if inFlight != 1 {
		t.Fatalf("same-key prompts must stay serialized: %d in flight", inFlight)
	}

	// Release the wedge: both PR-1 follow-ups deliver, in order.
	d.mu.Lock()
	d.block = nil
	d.mu.Unlock()
	close(gate)
	waitSends(t, d, 2)
	sent := d.sends()
	if !strings.Contains(sent[0], "work merge_conflict") || !strings.Contains(sent[1], "work failing_checks") {
		t.Fatalf("follow-up order: %v", sent)
	}
}
