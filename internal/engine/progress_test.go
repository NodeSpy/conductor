package engine

import (
	"context"
	"sync"
	"testing"
)

// A waiter that watches its place is told it as it starts waiting and each
// time the queue in front of it moves — counting higher-priority waiters,
// which are served first.
func TestSlotsReportQueuePosition(t *testing.T) {
	s := newSlots(1)
	if !s.acquire(context.Background(), prioNormal) {
		t.Fatal("first acquire")
	}
	var mu sync.Mutex
	seen := map[string][]int{}
	watch := func(name string) func(int) {
		return func(n int) {
			mu.Lock()
			seen[name] = append(seen[name], n)
			mu.Unlock()
		}
	}
	got := func(name string) []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), seen[name]...)
	}
	done := map[string]chan bool{}
	start := func(name string, prio int) {
		ch := make(chan bool, 1)
		done[name] = ch
		go func() { ch <- s.acquireWatch(context.Background(), prio, watch(name)) }()
	}
	start("a", prioNormal)
	waitQueued(t, s, 1)
	start("b", prioNormal)
	waitQueued(t, s, 2)
	// An interactive (high-priority) run jumps both.
	start("c", prioHigh)
	waitQueued(t, s, 3)
	waitCond(t, "a pushed back by c", func() bool { return eqInts(got("a"), []int{0, 1}) })
	waitCond(t, "b pushed back by c", func() bool { return eqInts(got("b"), []int{1, 2}) })
	if g := got("c"); !eqInts(g, []int{0}) {
		t.Fatalf("c = %v, want [0]", g)
	}
	s.release() // → c
	<-done["c"]
	waitCond(t, "a next", func() bool { return eqInts(got("a"), []int{0, 1, 0}) })
	waitCond(t, "b one ahead", func() bool { return eqInts(got("b"), []int{1, 2, 1}) })
	s.release() // → a
	<-done["a"]
	waitCond(t, "b next", func() bool { return eqInts(got("b"), []int{1, 2, 1, 0}) })
	s.release() // → b
	<-done["b"]
	// A run that never waited is told nothing.
	s.release()
	never := false
	if !s.acquireWatch(context.Background(), prioNormal, func(int) { never = true }) || never {
		t.Fatal("a free slot reported a queue place")
	}
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const progressCfg = `policy: { concurrency: { max_agents: 1 } }
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    hooks:
      - { at: progress, uses: eg.post, options: { text: "P {{.progress.state}}: {{.progress.text}}" } }
    steps:
      - { id: p, uses: eg.post, options: { text: "ran {{.msg}}" } }
`

// A run that waits for an agent slot says so on its progress hook at once,
// with its place in the queue — then runs once a slot frees, reporting
// itself running.
func TestQueuedRunReportsItsWait(t *testing.T) {
	eng, _, _, _ := buildFlowEngine(t, progressCfg)
	gateConnMu.Lock()
	gateConnCalls = nil
	gateConnMu.Unlock()
	if !eng.acquire(context.Background()) { // a long fixer holds the only slot
		t.Fatal("acquire")
	}
	first := flowTrigger("w1")
	first.Target.Number = 101
	first.Context = map[string]any{"msg": "w1"}
	eng.process(context.Background(), first)
	waitCond(t, "the first run's queued report", func() bool {
		return gateCallsWith("P queued: queued — waiting for an agent slot (next)") == 1
	})
	second := flowTrigger("w2")
	second.Target.Number = 102
	second.Context = map[string]any{"msg": "w2"}
	eng.process(context.Background(), second)
	waitCond(t, "the second run's queued report", func() bool {
		return gateCallsWith("P queued: queued — waiting for an agent slot (1 ahead)") == 1
	})
	if gateCallsWith("ran w1") != 0 {
		t.Fatal("ran while the cap was full")
	}
	eng.release()
	waitCond(t, "the first run", func() bool { return gateCallsWith("ran w1") == 1 })
	waitCond(t, "the first run's running report", func() bool {
		return gateCallsWith("P running: starting") >= 1 || gateCallsWith("P running: running step p") >= 1
	})
	waitCond(t, "the second run", func() bool { return gateCallsWith("ran w2") == 1 })
}
