package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// boundStep is resumeStep for a run bound to its target.
func boundStep() Request {
	var starts []AgentStart
	req := resumeStep(&starts)
	req.Trigger.Sem = &sdk.EventSemantics{BoundToTarget: true}
	return req
}

func shortBoundPoll(t *testing.T) {
	t.Helper()
	old := boundPoll
	boundPoll = 5 * time.Millisecond
	t.Cleanup(func() { boundPoll = old })
}

// dispatchAsync runs the dispatch and delivers its error.
func dispatchAsync(d *Dispatcher, req Request) <-chan error {
	errc := make(chan error, 1)
	go func() {
		_, err := d.Dispatch(context.Background(), req)
		errc <- err
	}()
	return errc
}

func wantStopped(t *testing.T, errc <-chan error) {
	t.Helper()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrTargetClosed) {
			t.Fatalf("Dispatch err = %v, want ErrTargetClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dispatch still waiting on an agent whose target closed")
	}
}

func (b *daemonBackend) counts() (agents, runs, archived, wsArchived int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.agents), len(b.runs), len(b.archived), len(b.wsArchived)
}

// When a target closes, the agent a foreground dispatch has running for a run
// bound to it is archived, and the dispatch ends as stopped rather than
// failed. Its run workspace goes with it. Another target's close, and a run
// not bound to its target, are left alone.
func TestPaseoStopTargetStopsTheBoundAgent(t *testing.T) {
	shortBoundPoll(t)
	b := newDaemonBackend()
	b.hang = true
	d := newResumeDispatcher(t, b)
	req := boundStep()
	errc := dispatchAsync(d, req)
	waitFor(t, "the agent to launch", func() bool { a, _, _, _ := b.counts(); return a == 1 })

	if n := d.StopTarget(context.Background(), "gh:acme/other#9"); n != 0 {
		t.Fatalf("stopped %d dispatches on another target", n)
	}
	if n := d.StopTarget(context.Background(), req.Trigger.Key()); n != 1 {
		t.Fatalf("StopTarget = %d, want 1", n)
	}
	wantStopped(t, errc)
	agents, _, archived, wsArchived := b.counts()
	if agents != 0 || archived != 1 {
		t.Fatalf("agents live=%d archived=%d, want the one agent archived", agents, archived)
	}
	if wsArchived != 1 {
		t.Fatalf("run workspaces archived = %d, want 1", wsArchived)
	}
	if n := d.StopTarget(context.Background(), req.Trigger.Key()); n != 0 {
		t.Fatalf("StopTarget after the dispatch ended = %d, want 0", n)
	}

	// Not bound to its target: nothing to stop.
	b2 := newDaemonBackend()
	b2.hang = true
	d2 := newResumeDispatcher(t, b2)
	var starts []AgentStart
	free := resumeStep(&starts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = d2.Dispatch(ctx, free) }()
	waitFor(t, "the unbound agent to launch", func() bool { a, _, _, _ := b2.counts(); return a == 1 })
	if n := d2.StopTarget(context.Background(), free.Trigger.Key()); n != 0 {
		t.Fatalf("stopped %d dispatches not bound to their target", n)
	}
	if a, _, archived, _ := b2.counts(); a != 1 || archived != 0 {
		t.Fatalf("an unbound agent was archived (live=%d archived=%d)", a, archived)
	}
}

// A target that closed before the step launches nothing; one that closes
// while the launch is being prepared is caught by the second ask, made once
// the dispatch can be stopped.
func TestPaseoDispatchAsksWhetherItsTargetClosed(t *testing.T) {
	shortBoundPoll(t)
	b := newDaemonBackend()
	d := newResumeDispatcher(t, b)
	req := boundStep()
	req.TargetClosed = func() bool { return true }
	wantStopped(t, dispatchAsync(d, req))
	if _, runs, _, _ := b.counts(); runs != 0 {
		t.Fatalf("%d agents launched for a target already closed", runs)
	}

	asked := 0
	req.TargetClosed = func() bool { asked++; return asked > 1 }
	wantStopped(t, dispatchAsync(d, req))
	if _, runs, _, _ := b.counts(); runs != 0 {
		t.Fatalf("%d agents launched for a target that closed as the launch began", runs)
	}

	// Still open: the launch goes ahead, labelled so a later close finds it.
	req.TargetClosed = func() bool { return false }
	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(b.runs))
	}
	found := false
	for _, l := range argAfter(b.runs[0], "--label") {
		if len(l) > len(boundLabel)+1 && l[:len(boundLabel)+1] == boundLabel+"=" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a bound launch carries no %s label: %v", boundLabel, argAfter(b.runs[0], "--label"))
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
