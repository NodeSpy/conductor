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

	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	fc.hang = true // every call (Invoke, and later Stop) blocks on ctx.Done()
	c := NewClient(reloadableSpec(t), Deps{dial: fakeDial(fc)})

	// Put a bounded call in flight that never returns (bounded only by its
	// own ctx, which we never cancel yet) — this instance is now "served",
	// and Reload's inflight.Wait() can never complete within
	// reloadDrainTimeout until cancelCall below lets it drain.
	callCtx, cancelCall := context.WithCancel(context.Background())
	invokeDone := make(chan struct{})
	started := make(chan struct{})
	go func() {
		defer close(invokeDone)
		close(started)
		_, _ = c.Invoke(callCtx, InvokeRequest{Instance: "x", Verb: "go"})
	}()
	<-started
	time.Sleep(20 * time.Millisecond) // let the call enter conn.Call (inflight++, served)

	reloadDone := make(chan struct{})
	go func() {
		defer close(reloadDone)
		_ = c.Reload(reloadableSpec(t)) // sets reloading=true, then parks on the undrainable inflight call
	}()
	time.Sleep(20 * time.Millisecond) // let Reload observe reloading and enter its drain wait

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("Close took %s — must return within ~stopGrace (%s), not wait out a stuck reload (reloadDrainTimeout=%s)",
				elapsed, stopGrace, reloadDrainTimeout)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("Close did not return within 500ms — blocked behind a stuck reload")
	}

	// Unblock and JOIN both background goroutines before touching the
	// package-level vars again (restoring them below) — otherwise the
	// Reload goroutine's own read of reloadDrainTimeout (its drain-wait
	// select) can still be running concurrently with this test function
	// returning and racing the restore, which -race correctly flags even
	// though cancelCall makes it finish almost immediately in practice:
	// "almost immediately" is not a happens-before edge.
	cancelCall()
	select {
	case <-invokeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the hung Invoke call never returned after cancelCall")
	}
	select {
	case <-reloadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the background Reload call never returned")
	}
	reloadDrainTimeout, stopGrace = oldDrain, oldGrace
}
