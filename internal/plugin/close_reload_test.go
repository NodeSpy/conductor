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

// TestClientCloseWakesCallParkedBehindStuckReloadNoDeadline is the other half
// of the finding-4 wiring: callFor's parked-on-reload wait loop is
//
//	for c.reloading && !c.closing && ctx.Err() == nil {
//	    c.reloadCond.Wait()
//	}
//
// A call bounded by context.Background() (no deadline, Done() == nil) spawns
// no ctx-watcher goroutine and never breaks out via ctx.Err(); if Reload is
// stuck draining (an earlier in-flight call that never returns) and never
// broadcasts either, the ONLY thing that can ever wake this parked call is
// Close() setting `closing` and broadcasting. Removing `!c.closing` from the
// wait condition means this call would instead hang until Reload's own
// reloadDrainTimeout fires (here, far longer than the assertion window
// below), so it fails this test.
func TestClientCloseWakesCallParkedBehindStuckReloadNoDeadline(t *testing.T) {
	oldDrain, oldGrace := reloadDrainTimeout, stopGrace
	reloadDrainTimeout = 2 * time.Second
	stopGrace = 100 * time.Millisecond

	fc := newFakeConn()
	fc.describe = &Decl{ProtocolVersion: ProtocolVersion, Type: "jira"}
	fc.hang = true // every call (Invoke, and later Stop) blocks on ctx.Done()
	c := NewClient(reloadableSpec(t), Deps{dial: fakeDial(fc)})

	// First call: bounded by its own cancellable ctx, never returns until
	// cancelCall — this is the in-flight call Reload can never drain.
	callCtx, cancelCall := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	firstStarted := make(chan struct{})
	go func() {
		defer close(firstDone)
		close(firstStarted)
		_, _ = c.Invoke(callCtx, InvokeRequest{Instance: "x", Verb: "go"})
	}()
	<-firstStarted
	time.Sleep(20 * time.Millisecond) // let it enter conn.Call (inflight++, served)

	reloadDone := make(chan struct{})
	go func() {
		defer close(reloadDone)
		_ = c.Reload(reloadableSpec(t)) // sets reloading=true, then parks undrainable
	}()
	time.Sleep(20 * time.Millisecond) // let Reload observe reloading and enter its drain wait

	// Second call: NO ctx deadline at all — context.Background(). It parks in
	// callFor's reloading-wait loop with no watcher goroutine and no way to
	// observe ctx ending, since there is nothing to end.
	secondErr := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		_, err := c.Invoke(context.Background(), InvokeRequest{Instance: "x", Verb: "go2"})
		secondErr <- err
	}()
	<-secondStarted
	time.Sleep(20 * time.Millisecond) // let it enter callFor and park on reloadCond

	start := time.Now()
	go c.Close()
	select {
	case err := <-secondErr:
		if err == nil {
			t.Error("expected the parked no-deadline call to return an error, got nil")
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("parked call took %s to wake after Close — must be woken promptly by `closing`, not wait out reloadDrainTimeout (%s)",
				elapsed, reloadDrainTimeout)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the no-deadline call parked behind the stuck reload never returned after Close")
	}

	cancelCall()
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the hung first Invoke call never returned after cancelCall")
	}
	select {
	case <-reloadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the background Reload call never returned")
	}
	reloadDrainTimeout, stopGrace = oldDrain, oldGrace
}
