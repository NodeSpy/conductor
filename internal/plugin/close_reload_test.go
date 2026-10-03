package plugin

import (
	"context"
	"testing"
	"time"
)

// TestClientCloseDuringStuckReloadReturnsPromptly is the finding-4 regression:
// Close must not wait on a Reload that is stuck draining (an in-flight call
// that never returns, so Reload parks until reloadDrainTimeout). Before the
// fix, Close -> stopServed -> Stop (for the instance the hung Invoke marked
// served) parked on reloadCond with no regard for ctx or Close itself, so
// Close took as long as the stuck reload — up to reloadDrainTimeout, ignoring
// stopServed's much shorter stopGrace deadline entirely.
func TestClientCloseDuringStuckReloadReturnsPromptly(t *testing.T) {
	oldDrain, oldGrace := reloadDrainTimeout, stopGrace
	reloadDrainTimeout = 2 * time.Second
	stopGrace = 100 * time.Millisecond
	defer func() { reloadDrainTimeout, stopGrace = oldDrain, oldGrace }()

	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	fc.hang = true // every call (Invoke, and later Stop) blocks on ctx.Done()
	c := NewClient(reloadableSpec(t), Deps{dial: fakeDial(fc)})

	// Put a bounded call in flight that never returns (bounded only by its
	// own ctx, which we never cancel) — this instance is now "served", and
	// Reload's inflight.Wait() can never complete within reloadDrainTimeout.
	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = c.Invoke(callCtx, InvokeRequest{Instance: "x", Verb: "go"})
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // let the call enter conn.Call (inflight++, served)

	go func() { _ = c.Reload(reloadableSpec(t)) }() // sets reloading=true, then parks on the undrainable inflight call
	time.Sleep(20 * time.Millisecond)               // let Reload observe reloading and enter its drain wait

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("Close took %s — must return within ~stopGrace (%s), not wait out a stuck reload (reloadDrainTimeout=%s)",
				elapsed, stopGrace, reloadDrainTimeout)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not return within 500ms — blocked behind a stuck reload")
	}
}
