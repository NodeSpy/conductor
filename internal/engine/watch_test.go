package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

func TestMatchWatch(t *testing.T) {
	scope := map[string]any{
		"pr": map[string]any{"merged": true, "state": "open", "review_decision": "APPROVED"},
		"handoff": map[string]any{
			"pr": map[string]any{"head_sha": "aaa"},
		},
	}
	t.Run("literal match fires", func(t *testing.T) {
		rule, ok := matchWatch([]config.WatchRule{{If: "pr.merged == true", Uses: "handoff.bail"}}, scope, nil)
		if !ok || rule.Uses != "handoff.bail" {
			t.Fatalf("want bail, got %v ok=%v", rule, ok)
		}
	})
	t.Run("first matching rule wins", func(t *testing.T) {
		rule, ok := matchWatch([]config.WatchRule{
			{If: "pr.state == \"closed\"", Uses: "handoff.first"},
			{If: "pr.review_decision == \"APPROVED\"", Uses: "handoff.second"},
		}, scope, nil)
		if !ok || rule.Uses != "handoff.second" {
			t.Fatalf("want second, got %v ok=%v", rule, ok)
		}
	})
	t.Run("empty if always matches", func(t *testing.T) {
		rule, ok := matchWatch([]config.WatchRule{{Uses: "handoff.always"}}, scope, nil)
		if !ok || rule.Uses != "handoff.always" {
			t.Fatalf("want always, got %v ok=%v", rule, ok)
		}
	})
	t.Run("no match", func(t *testing.T) {
		if _, ok := matchWatch([]config.WatchRule{{If: "pr.merged == false", Uses: "x"}}, scope, nil); ok {
			t.Fatal("did not expect a match")
		}
	})
	t.Run("eval error skips the rule, does not fire", func(t *testing.T) {
		var gotErr bool
		_, ok := matchWatch([]config.WatchRule{{If: "contains(pr.state)", Uses: "handoff.bail"}}, scope,
			func(string, error) { gotErr = true })
		if ok {
			t.Fatal("a broken condition must never fire an action")
		}
		if !gotErr {
			t.Fatal("expected the eval error to be reported")
		}
	})
}

func (n *fakeNotif) count() int { n.mu.Lock(); defer n.mu.Unlock(); return len(n.events) }

// fakePRImpl returns scripted pr_get outputs — one per Invoke, clamping to the
// last once exhausted. Used to drive a watch condition from false to true.
type fakePRImpl struct {
	calls  int32
	states []map[string]any
}

func (f *fakePRImpl) Validate() error          { return nil }
func (f *fakePRImpl) DeclaredEvents() []string { return nil }
func (f *fakePRImpl) Source([]connector.CompiledTrigger) (core.Integration, error) {
	return nil, nil
}
func (f *fakePRImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	i := int(atomic.AddInt32(&f.calls, 1)) - 1
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	return f.states[i], nil
}

var fakePRSingleton *fakePRImpl

func init() {
	connector.RegisterType(&connector.TypeDecl{
		Type: "fakepr",
		Verbs: []connector.VerbDecl{
			{Name: "pr_get", Outputs: connector.Schema{"merged": {Type: connector.TBool}}},
		},
	}, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
		return fakePRSingleton, nil
	})
}

func TestHandoffWatchBails(t *testing.T) {
	fakePRSingleton = &fakePRImpl{states: []map[string]any{
		{"merged": false}, // snapshot read
		{"merged": true},  // first tick → bail
	}}
	reg, err := connector.Build(&config.Config{
		ConnectorsMap: map[string]config.ConnectorRef{"fp": {Use: "fakepr"}},
	}, connector.Deps{Log: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	nf := &fakeNotif{}
	e := &Engine{
		log:        func(string, ...any) {},
		notif:      nf,
		hold:       dispatch.NewHoldSet(""),
		broker:     controller.NewBroker(nil, nil, func(string, ...any) {}),
		connectors: reg,
	}
	trig := core.Trigger{Target: core.Target{Repo: "o/r", Number: 7}}
	profile := config.Step{Watch: &config.WatchSpec{
		Uses:  "fp.pr_get",
		As:    "pr",
		Every: config.Duration(2 * time.Millisecond),
		On: []config.WatchRule{
			{If: "pr.merged == true", Uses: "handoff.bail", Options: map[string]any{"notify": "PR merged — closing review"}},
		},
	}}
	e.hold.Add("agent-1")

	runCtx, cancel := context.WithCancel(context.Background())
	e.startHandoffWatch(context.Background(), runCtx, cancel, trig, "review", "agent-1", trig.Key(), profile)

	waitFor(t, func() bool { return !e.hold.Has("agent-1") })
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("bail did not cancel the review ctx")
	}
	if nf.count() == 0 {
		t.Fatal("bail did not notify the channel")
	}
}
