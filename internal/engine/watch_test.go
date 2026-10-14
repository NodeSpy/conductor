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
	"github.com/NodeSpy/conductor/internal/skill"
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
	disp := &fakeDispatcher{archived: make(chan string, 1)}
	e := &Engine{
		log:        func(string, ...any) {},
		hold:       dispatch.NewHoldSet(""),
		broker:     controller.NewBroker(nil, nil, func(string, ...any) {}),
		connectors: reg,
		disp:       disp,
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
	// bail must reclaim the agent itself: dropping the hold alone strands the
	// workspace (nothing sweeps released hand-offs).
	select {
	case id := <-disp.archived:
		if id != "agent-1" {
			t.Fatalf("bail archived %q, want agent-1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("bail did not archive the agent")
	}
}

// TestNativeHandoffLifecycle: a background step with no ask channel (the agent
// is left live in paseo) still gets its watch:/idle_timeout: and a live-registry
// entry. Before, the no-channel path only emitted needs_input — watch rules
// never ran and idle_timeout never fired, so the hand-off lingered forever.
func TestNativeHandoffLifecycle(t *testing.T) {
	disp := &fakeDispatcher{archived: make(chan string, 1)}
	e := &Engine{
		log:   func(string, ...any) {},
		notif: &fakeNotif{},
		hold:  dispatch.NewHoldSet(""),
		disp:  disp,
	}
	trig := core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "o/r", Number: 9}}
	profile := config.Step{Background: true, IdleTimeout: config.Duration(20 * time.Millisecond)}
	e.flowAgentServices().Background(context.Background(), trig, "handoff", "rev", profile,
		dispatch.RunRef{AgentID: "n1"}, "", dispatch.HandoffActions{})

	if lh := e.lookupLiveHandoff("n1"); lh == nil || lh.prKey != trig.Key() || lh.kind != "review_requested" {
		t.Fatalf("no-channel hand-off was not registered live: %+v", lh)
	}
	if !e.hold.Has("n1") {
		t.Fatal("no-channel hand-off was not held")
	}
	select {
	case id := <-disp.archived:
		if id != "n1" {
			t.Fatalf("idle timeout archived %q, want n1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("idle_timeout never released the no-channel hand-off")
	}
	waitFor(t, func() bool { return e.lookupLiveHandoff("n1") == nil && !e.hold.Has("n1") })
}

func TestHandoffDoneReleases(t *testing.T) {
	disp := &fakeDispatcher{archived: make(chan string, 1)}
	e := &Engine{
		log:    func(string, ...any) {},
		hold:   dispatch.NewHoldSet(""),
		broker: controller.NewBroker(nil, nil, func(string, ...any) {}),
		disp:   disp,
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.hold.Add("a1")
	e.registerLiveHandoff("a1", cancel, "o/r#1", "", "review")

	if err := e.handoffDone(context.Background(), "a1", "test"); err != nil {
		t.Fatalf("handoffDone: %v", err)
	}
	// done must ACTIVELY reclaim the agent — a hand-off carries no archive=1
	// label, so the reaper never would (the bug this guards against).
	select {
	case id := <-disp.archived:
		if id != "a1" {
			t.Fatalf("done archived %q, want a1", id)
		}
	default:
		t.Fatal("done did not archive the agent — it would linger forever")
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
	e.registerLiveHandoff("a2", cancel, "o/r#2", "", "review")
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
			e.registerLiveHandoff("old", cancel, trig.Key(), "", "review")
			e.startHandoffWatch(context.Background(), runCtx, cancel, trig, "review", "rev", "old", trig.Key(), profile, nil, tc.actions(&hit))

			// head moved → supersede fires: tears the stale hand-off down (hold
			// released) and invokes the action closure.
			waitFor(t, func() bool { return atomic.LoadInt32(&hit) > 0 })
			waitFor(t, func() bool { return !e.hold.Has("old") })
		})
	}
}

// TestReleaseRevokesHandoffSession: a released hand-off's conductor session
// token dies with it — it is durable (persisted, sliding) only while the
// hand-off is open.
func TestReleaseRevokesHandoffSession(t *testing.T) {
	b := skill.NewBroker(nil, nil)
	prev := skill.Active()
	skill.SetActive(b)
	defer skill.SetActive(prev)
	tok, err := b.MintHandoffSession(skill.Identity{Dispatch: "d-9"}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	peer := skill.Peer{UID: 1000, Valid: true}
	if _, err := b.Authorize(tok, peer); err != nil {
		t.Fatal(err)
	}
	disp := &fakeDispatcher{archived: make(chan string, 1), agentDisps: map[string][]string{"a9": {"d-9"}}}
	e := &Engine{log: func(string, ...any) {}, hold: dispatch.NewHoldSet(""), disp: disp}
	_, cancel := context.WithCancel(context.Background())
	e.registerLiveHandoff("a9", cancel, "o/r#9", "review_requested", "handoff")
	if err := e.handoffDone(context.Background(), "a9", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Authorize(tok, peer); err == nil {
		t.Fatal("a released hand-off's session token still authorizes")
	}
}
