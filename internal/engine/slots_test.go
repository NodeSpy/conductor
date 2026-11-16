package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// acquireAsync starts an acquire and reports its result on the returned channel.
func acquireAsync(s *slots, prio int) <-chan bool {
	ch := make(chan bool, 1)
	go func() { ch <- s.acquire(context.Background(), prio) }()
	return ch
}

// waitQueued blocks until n acquires are parked in the pool.
func waitQueued(t *testing.T, s *slots, n int) {
	t.Helper()
	waitCond(t, "queued waiters", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.queued() == n
	})
}

func TestSlotsHighPriorityJumpsTheQueue(t *testing.T) {
	s := newSlots(1)
	if !s.acquire(context.Background(), prioNormal) {
		t.Fatal("first acquire failed")
	}
	normal := acquireAsync(s, prioNormal)
	waitQueued(t, s, 1)
	high := acquireAsync(s, prioHigh)
	waitQueued(t, s, 2)

	s.release() // the freed slot goes to the high-priority waiter, not the older normal one
	select {
	case <-high:
	case <-time.After(time.Second):
		t.Fatal("high-priority waiter was not served first")
	}
	select {
	case <-normal:
		t.Fatal("normal waiter got a slot while the cap was full")
	case <-time.After(30 * time.Millisecond):
	}
	s.release()
	select {
	case <-normal:
	case <-time.After(time.Second):
		t.Fatal("normal waiter never served")
	}
}

func TestSlotsFIFOWithinAPriority(t *testing.T) {
	s := newSlots(1)
	s.acquire(context.Background(), prioNormal)
	first := acquireAsync(s, prioNormal)
	waitQueued(t, s, 1)
	second := acquireAsync(s, prioNormal)
	waitQueued(t, s, 2)
	s.release()
	select {
	case <-first:
	case <-second:
		t.Fatal("later waiter served before the earlier one")
	case <-time.After(time.Second):
		t.Fatal("no waiter served")
	}
}

func TestSlotsCancelledWaiterDoesNotLeakTheSlot(t *testing.T) {
	s := newSlots(1)
	s.acquire(context.Background(), prioNormal)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() { res <- s.acquire(ctx, prioNormal) }()
	waitQueued(t, s, 1)
	cancel()
	if <-res {
		t.Fatal("cancelled acquire reported success")
	}
	s.release()
	// The only slot is free again: a fresh acquire succeeds without waiting.
	done := acquireAsync(s, prioNormal)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("acquire failed")
		}
	case <-time.After(time.Second):
		t.Fatal("slot leaked by the cancelled waiter")
	}
	if s.full() != true {
		t.Fatal("cap 1 with one holder should be full")
	}
}

// With every slot held, processing a flow trigger must return at once — the
// wait belongs to the run's goroutine, not the engine loop — and the run starts
// as soon as a slot frees.
func TestFlowWaitForSlotDoesNotBlockTheLoop(t *testing.T) {
	cfg := "policy: { concurrency: { max_agents: 1 } }\n" + gateCfg
	eng, _, _, _ := buildFlowEngine(t, cfg)
	if !eng.acquire(context.Background()) { // a long fixer holds the only slot
		t.Fatal("acquire")
	}
	before := gateCalls()
	returned := make(chan struct{})
	go func() {
		eng.process(context.Background(), flowTrigger("s1"))
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("process blocked the engine loop waiting for a slot")
	}
	time.Sleep(30 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("flow ran while the cap was full")
	}
	eng.release()
	waitCond(t, "flow run after the slot freed", func() bool { return gateCalls() > before })
}

// A second trigger for a flow+target that is already waiting folds into it:
// one run, carrying the newest event.
func TestFlowWaitingRunCoalescesNewestEvent(t *testing.T) {
	cfg := "policy: { concurrency: { max_agents: 1 } }\n" + gateCfg
	eng, _, _, _ := buildFlowEngine(t, cfg)
	eng.acquire(context.Background())
	gateConnMu.Lock()
	gateConnCalls = nil
	gateConnMu.Unlock()

	older := flowTrigger("c1")
	older.Context = map[string]any{"msg": "older"}
	newer := flowTrigger("c2")
	newer.Context = map[string]any{"msg": "newer"}
	eng.process(context.Background(), older)
	eng.process(context.Background(), newer)
	eng.release()

	waitCond(t, "coalesced run", func() bool { return gateCalls() == 1 })
	time.Sleep(50 * time.Millisecond)
	gateConnMu.Lock()
	defer gateConnMu.Unlock()
	if len(gateConnCalls) != 1 {
		t.Fatalf("runs: %d, want 1 (the waiting duplicate should coalesce)", len(gateConnCalls))
	}
	if got, _ := gateConnCalls[0]["text"].(string); !strings.Contains(got, "newer") {
		t.Fatalf("ran with %q, want the newest event", got)
	}
}

// A fixer that was waiting for a slot when its PR merged must not run: there
// is no branch left to push to (it would re-create the deleted branch, or open
// a follow-up PR nobody asked for). A trigger queued after the close — a
// reopened PR — still runs.
func TestFlowWaitingFixerDroppedWhenPRCloses(t *testing.T) {
	cfg := "policy: { concurrency: { max_agents: 1 } }\n" + gateCfg
	eng, _, _, _ := buildFlowEngine(t, cfg)
	eng.acquire(context.Background())
	before := gateCalls()

	fix := flowTrigger("fix1")
	fix.Kind = "new_comment"
	eng.process(context.Background(), fix)
	closed := flowTrigger("")
	closed.Kind = "_closed"
	closed.Action = nil // an unrouted closing event carries no trigger's work
	eng.process(context.Background(), closed)
	eng.release()

	time.Sleep(100 * time.Millisecond)
	if gateCalls() != before {
		t.Fatal("a fixer queued before the PR closed still ran")
	}

	// Queued after the close (the PR was reopened): runs normally.
	again := flowTrigger("fix2")
	again.Kind = "new_comment"
	eng.process(context.Background(), again)
	waitCond(t, "fixer queued after the close", func() bool { return gateCalls() > before })
}

// stopRecDispatcher is a runtime that records StopTarget calls.
type stopRecDispatcher struct {
	fakeFlowDispatcher
	mu      sync.Mutex
	stopped []string
}

func (d *stopRecDispatcher) StopTarget(_ context.Context, key string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = append(d.stopped, key)
	return 1
}

// When a PR's _closed arrives, the engine asks every runtime to stop the
// fixers still running on it.
func TestClosedPRStopsRunningFixers(t *testing.T) {
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
	st := newFlowGateStore()
	d := &stopRecDispatcher{}
	eng := New(Options{Config: &cfg, Store: st, Dispatch: d, Notifier: &fakeNotif{},
		Flow: flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st}), Connectors: reg})

	closed := flowTrigger("")
	closed.Kind = "_closed"
	closed.Action = nil // an unrouted closing event carries no trigger's work
	eng.process(context.Background(), closed)

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.stopped) != 1 || d.stopped[0] != closed.Key() {
		t.Fatalf("StopTarget calls = %v, want one for %s", d.stopped, closed.Key())
	}
}

// reqRecDispatcher is a runtime that keeps the requests it was handed.
type reqRecDispatcher struct {
	fakeFlowDispatcher
	mu   sync.Mutex
	reqs []dispatch.Request
}

func (d *reqRecDispatcher) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = append(d.reqs, req)
	return dispatch.RunRef{}, nil
}

// stopFixers stops the sessions live when a close arrives. A run that is
// between steps, or whose session is still coming up, has none: its dispatch
// carries the means to ask (Request.TargetClosed), true once the target closed
// at or after the run began. A run begun after the close (a reopened target)
// and a run not bound to its target are never told so.
func TestDispatchLearnsItsTargetClosedUnderTheRun(t *testing.T) {
	eng, _, _, _ := buildFlowEngine(t, gateCfg)
	d := &reqRecDispatcher{}
	fix := flowTrigger("fix1")
	fix.Kind = "new_comment"
	if !fix.BoundToTarget() {
		t.Fatal("test trigger is not bound to its target")
	}
	began := time.Now()
	send := func(ctx context.Context, tr core.Trigger) dispatch.Request {
		t.Helper()
		if _, err := eng.dispatchAgent(ctx, d, dispatch.Request{Trigger: tr}); err != nil {
			t.Fatal(err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.reqs[len(d.reqs)-1]
	}

	req := send(withRunSince(context.Background(), began), fix)
	if req.TargetClosed == nil {
		t.Fatal("a bound run's dispatch cannot ask whether its target closed")
	}
	if req.TargetClosed() {
		t.Fatal("target reported closed before any close")
	}
	eng.markClosed(fix.Key())
	if !req.TargetClosed() {
		t.Fatal("target closed under the run, and its dispatch was not told")
	}

	// Another target's close is not this run's.
	other := fix
	other.Target.Number = 2
	if send(withRunSince(context.Background(), began), other).TargetClosed() {
		t.Fatal("another target's close stopped this run")
	}
	// Begun after the close: the target was reopened.
	if send(withRunSince(context.Background(), time.Now().Add(time.Second)), fix).TargetClosed() {
		t.Fatal("a run begun after the close was told its target closed")
	}
	// Not bound to its target, or not a flow run: nothing to ask.
	if send(withRunSince(context.Background(), began), flowTrigger("p1")).TargetClosed != nil {
		t.Fatal("a run not bound to its target was given a close to watch")
	}
	if send(context.Background(), fix).TargetClosed != nil {
		t.Fatal("a dispatch outside a flow run was given a close to watch")
	}
}
