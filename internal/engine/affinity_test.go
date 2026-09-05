package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

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
	mu   sync.Mutex
	sent []string
}

func (d *sendingDispatcher) Send(_ context.Context, id, prompt string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, id+"\x00"+prompt)
	return nil
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
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"pr-agent": {Provider: "claude", Session: &config.SessionSpec{
			Key:   "{{.repo}}#{{.number}}",
			EndOn: []string{"i.pr_closed"},
		}},
		"fresh-agent": {Provider: "claude"},
	}}
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
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h1", "s1", act("pr-agent")))
	if len(d.reqs) != 1 {
		t.Fatalf("first event must spawn: %d dispatches", len(d.reqs))
	}
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 1, "h2", "s2", act("pr-agent")))
	e.process(context.Background(), agentTrigger("failing_checks", "a/w", 1, "h3", "s3", act("pr-agent")))
	if len(d.reqs) != 1 {
		t.Fatalf("same-key events must not spawn again: %d dispatches", len(d.reqs))
	}
	d.mu.Lock()
	sent := append([]string(nil), d.sent...)
	d.mu.Unlock()
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "agent-1\x00") || !strings.Contains(sent[0], "work merge_conflict") {
		t.Fatalf("follow-ups: %v", sent)
	}

	// A different PR spawns its own session.
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 2, "h4", "s4", act("pr-agent")))
	if len(d.reqs) != 2 {
		t.Fatalf("a new key must spawn: %d dispatches", len(d.reqs))
	}

	// A profile without session: dispatches fresh every time.
	e.process(context.Background(), agentTrigger("new_comment", "a/w", 3, "h5", "s5", act("fresh-agent")))
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 3, "h6", "s6", act("fresh-agent")))
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
	act := config.Action{Type: "agent", Agent: "pr-agent", Prompt: "work"}

	e.process(context.Background(), agentTrigger("new_comment", "a/w", 1, "h1", "s1", act))
	if !e.affinityOwns("agent-1") {
		t.Fatal("session must be bound")
	}
	// pr_closed arrives (agentTrigger uses instance "i" — matched by
	// end_on "i.pr_closed"). It carries no action the engine would run;
	// eviction happens at observe time.
	closed := core.Trigger{Source: "github", Instance: "i", Kind: "pr_closed",
		Target: core.Target{Repo: "a/w", PR: 1, Number: 1}}
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
