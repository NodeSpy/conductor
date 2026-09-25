package engine

import (
	"context"
	"strings"
	"testing"
	"time"
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
	cfg := "control: { max_concurrent_agents: 1 }\n" + gateCfg
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
	cfg := "control: { max_concurrent_agents: 1 }\n" + gateCfg
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
