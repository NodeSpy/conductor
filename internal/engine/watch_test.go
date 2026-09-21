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
		st, ok := matchWatch([]config.Step{{If: "pr.merged == true", Uses: "handoff.bail"}}, scope, nil)
		if !ok || st.Uses != "handoff.bail" {
			t.Fatalf("want bail, got %v ok=%v", st, ok)
		}
	})
	t.Run("first matching step wins", func(t *testing.T) {
		st, ok := matchWatch([]config.Step{
			{If: "pr.state == \"closed\"", Uses: "handoff.first"},
			{If: "pr.review_decision == \"APPROVED\"", Uses: "handoff.second"},
		}, scope, nil)
		if !ok || st.Uses != "handoff.second" {
			t.Fatalf("want second, got %v ok=%v", st, ok)
		}
	})
	t.Run("empty if always matches", func(t *testing.T) {
		st, ok := matchWatch([]config.Step{{Uses: "handoff.always"}}, scope, nil)
		if !ok || st.Uses != "handoff.always" {
			t.Fatalf("want always, got %v ok=%v", st, ok)
		}
	})
	t.Run("no match", func(t *testing.T) {
		if _, ok := matchWatch([]config.Step{{If: "pr.merged == false", Uses: "x"}}, scope, nil); ok {
			t.Fatal("did not expect a match")
		}
	})
	t.Run("eval error skips the step, does not fire", func(t *testing.T) {
		var gotErr bool
		_, ok := matchWatch([]config.Step{{If: "contains(pr.state)", Uses: "handoff.bail"}}, scope,
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
	e := &Engine{
		log:        func(string, ...any) {},
		hold:       dispatch.NewHoldSet(""),
		broker:     controller.NewBroker(nil, nil, func(string, ...any) {}),
		connectors: reg,
	}
	trig := core.Trigger{TargetTrusted: true, Target: core.Target{Repo: "o/r", Number: 7}}
	profile := config.Step{Watch: &config.WatchSpec{
		Every: config.Duration(2 * time.Millisecond),
		Steps: []config.Step{
			{ID: "pr", Uses: "fp.pr_get"},
			{If: "pr.merged == true", Uses: "handoff.bail"},
		},
	}}
	e.hold.Add("agent-1")

	runCtx, cancel := context.WithCancel(context.Background())
	e.startHandoffWatch(context.Background(), runCtx, cancel, trig, "review", "rev", "agent-1", trig.Key(), profile, nil, dispatch.HandoffActions{})

	waitFor(t, func() bool { return !e.hold.Has("agent-1") })
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("bail did not cancel the review ctx")
	}
}

func TestHandoffDoneReleases(t *testing.T) {
	e := &Engine{
		log:    func(string, ...any) {},
		hold:   dispatch.NewHoldSet(""),
		broker: controller.NewBroker(nil, nil, func(string, ...any) {}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.hold.Add("a1")
	e.registerLiveHandoff("a1", cancel, "o/r#1", "review")

	if err := e.handoffDone(context.Background(), "a1", "test"); err != nil {
		t.Fatalf("handoffDone: %v", err)
	}
	if e.hold.Has("a1") {
		t.Fatal("done did not release the reaper hold")
	}
	if ctx.Err() == nil {
		t.Fatal("done did not cancel the review ctx")
	}
	if e.lookupLiveHandoff("a1") != nil {
		t.Fatal("done did not deregister the live hand-off")
	}
	if err := e.handoffDone(context.Background(), "a1", "again"); err == nil {
		t.Fatal("a second done on a released hand-off should error")
	}
}

func TestIdleTimerReleases(t *testing.T) {
	e := &Engine{
		log:    func(string, ...any) {},
		hold:   dispatch.NewHoldSet(""),
		broker: controller.NewBroker(nil, nil, func(string, ...any) {}),
	}
	runCtx, cancel := context.WithCancel(context.Background())
	e.hold.Add("a2")
	e.registerLiveHandoff("a2", cancel, "o/r#2", "review")
	e.startIdleTimer(context.Background(), runCtx, core.Trigger{}, "review", "a2", 5*time.Millisecond)

	// idle elapses → the hand-off is released (hold dropped).
	waitFor(t, func() bool { return !e.hold.Has("a2") })
}

// TestHandoffWatchSupersede drives a head change through both supersede actions
// (run_workflow and rerun_step) and asserts each tears the stale hand-off down
// and invokes the matching HandoffActions closure.
func TestHandoffWatchSupersede(t *testing.T) {
	cases := []struct {
		name    string
		action  config.Step
		actions func(hit *int32) dispatch.HandoffActions
	}{
		{
			name:   "workflow step",
			action: config.Step{If: "pr.head_sha != handoff.pr.head_sha", Workflow: "review-flow"},
			actions: func(hit *int32) dispatch.HandoffActions {
				return dispatch.HandoffActions{RunWorkflow: func(_ context.Context, wf string, _ map[string]any) error {
					if wf == "review-flow" {
						atomic.AddInt32(hit, 1)
					}
					return nil
				}}
			},
		},
		{
			name:   "rerun",
			action: config.Step{If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.rerun"},
			actions: func(hit *int32) dispatch.HandoffActions {
				return dispatch.HandoffActions{RerunStep: func(_ context.Context, _ string) error {
					atomic.AddInt32(hit, 1)
					return nil
				}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakePRSingleton = &fakePRImpl{states: []map[string]any{
				{"head_sha": "aaa"}, // snapshot
				{"head_sha": "bbb"}, // moved → supersede
			}}
			reg, err := connector.Build(&config.Config{
				ConnectorsMap: map[string]config.ConnectorRef{"fp": {Use: "fakepr"}},
			}, connector.Deps{Log: func(string, ...any) {}})
			if err != nil {
				t.Fatalf("build registry: %v", err)
			}
			e := &Engine{
				log:        func(string, ...any) {},
				notif:      &fakeNotif{},
				hold:       dispatch.NewHoldSet(""),
				broker:     controller.NewBroker(nil, nil, func(string, ...any) {}),
				connectors: reg,
				disp:       &stepFake{}, // archive of the stale agent lands here
			}
			trig := core.Trigger{TargetTrusted: true, Target: core.Target{Repo: "o/r", Number: 7}}
			profile := config.Step{Watch: &config.WatchSpec{
				Every: config.Duration(2 * time.Millisecond),
				Steps: []config.Step{{ID: "pr", Uses: "fp.pr_get"}, tc.action},
			}}
			e.hold.Add("old")
			var hit int32
			runCtx, cancel := context.WithCancel(context.Background())
			e.registerLiveHandoff("old", cancel, trig.Key(), "review")
			e.startHandoffWatch(context.Background(), runCtx, cancel, trig, "review", "rev", "old", trig.Key(), profile, nil, tc.actions(&hit))

			// head moved → supersede fires: tears the stale hand-off down (hold
			// released) and invokes the action closure.
			waitFor(t, func() bool { return atomic.LoadInt32(&hit) > 0 })
			waitFor(t, func() bool { return !e.hold.Has("old") })
		})
	}
}
