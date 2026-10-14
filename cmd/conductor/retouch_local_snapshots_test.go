package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestRetouchLocalPluginSnapshotsLoopTicksAndStopsOnCtx is finding 5(d)'s
// ticker-logic test: retouchLocalPluginSnapshotsLoop must call its action
// repeatedly on the given interval, and stop promptly once ctx is cancelled
// — never leaking a goroutine that keeps firing after shutdown. A short
// interval stands in for the injectable clock: the loop takes interval and
// the action as plain parameters, so nothing here depends on real Managers,
// snapshots, or the production 24h cadence.
func TestRetouchLocalPluginSnapshotsLoopTicksAndStopsOnCtx(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		retouchLocalPluginSnapshotsLoop(ctx, 5*time.Millisecond, func() { atomic.AddInt32(&calls, 1) })
		close(done)
	}()

	// Several ticks' worth of real time: the action must have fired more
	// than once (it is not a one-shot).
	time.Sleep(60 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n < 2 {
		t.Fatalf("expected the action to have fired at least twice by now, got %d", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retouchLocalPluginSnapshotsLoop did not return promptly after ctx was cancelled")
	}

	// No further calls land after cancellation.
	n := atomic.LoadInt32(&calls)
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != n {
		t.Fatalf("the action fired again (%d -> %d) after ctx was cancelled", n, got)
	}
}

// TestRetouchLocalPluginSnapshotsLoopZeroIntervalIsNoop guards against a
// misconfigured interval (<= 0) spinning a busy ticker goroutine forever —
// time.NewTicker itself panics on a non-positive duration.
func TestRetouchLocalPluginSnapshotsLoopZeroIntervalIsNoop(t *testing.T) {
	done := make(chan struct{})
	go func() {
		retouchLocalPluginSnapshotsLoop(context.Background(), 0, func() { t.Error("action must never run with a zero interval") })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retouchLocalPluginSnapshotsLoop(interval=0) must return immediately, not block")
	}
}
