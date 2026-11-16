package dispatch

import (
	"context"
	"errors"
	"strings"
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

func (b *daemonBackend) counts() (agents, runs, archived, wsArchived int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.agents), len(b.runs), len(b.archived), len(b.wsArchived)
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

// A run bound to its target is cancelled with cause ErrTargetClosed when the
// target closes. That ends the `paseo run` conductor waits on, not the agent
// in the paseo daemon: the dispatch archives it, and the run workspace with
// it, and ends as stopped. A cancellation for any other reason (a shutdown)
// keeps both for the restart, and a run not bound to its target is never
// labelled or archived this way.
func TestPaseoArchivesTheAgentOfARunWhoseTargetClosed(t *testing.T) {
	shortBoundPoll(t)
	b := newDaemonBackend()
	b.hang = true
	d := newResumeDispatcher(t, b)
	ctx, stop := context.WithCancelCause(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := d.Dispatch(ctx, boundStep())
		errc <- err
	}()
	waitFor(t, "the agent to launch", func() bool { a, _, _, _ := b.counts(); return a == 1 })
	b.mu.Lock()
	labelled := false
	for _, l := range argAfter(b.runs[0], "--label") {
		labelled = labelled || strings.HasPrefix(l, boundLabel+"=")
	}
	b.mu.Unlock()
	if !labelled {
		t.Fatalf("a bound launch carries no %s label", boundLabel)
	}

	stop(ErrTargetClosed) // the target closes
	select {
	case err := <-errc:
		if !errors.Is(err, ErrTargetClosed) {
			t.Fatalf("Dispatch err = %v, want ErrTargetClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dispatch still waiting on an agent whose target closed")
	}
	if agents, _, archived, wsArchived := b.counts(); agents != 0 || archived != 1 || wsArchived != 1 {
		t.Fatalf("live=%d archived=%d workspaces archived=%d, want the agent and its run workspace archived",
			agents, archived, wsArchived)
	}

	// Already closed when the step starts: nothing is launched.
	if _, err := d.Dispatch(ctx, boundStep()); !errors.Is(err, ErrTargetClosed) {
		t.Fatalf("Dispatch on a closed target: err = %v, want ErrTargetClosed", err)
	}
	if _, runs, _, _ := b.counts(); runs != 1 {
		t.Fatalf("%d launches, want none for a target already closed", runs-1)
	}

	// A shutdown is not a close: the agent and workspace stay for the restart.
	b2 := newDaemonBackend()
	b2.hang = true
	d2 := newResumeDispatcher(t, b2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		_, err := d2.Dispatch(ctx2, boundStep())
		errc <- err
	}()
	waitFor(t, "the second agent to launch", func() bool { a, _, _, _ := b2.counts(); return a == 1 })
	cancel2()
	if err := <-errc; err == nil || errors.Is(err, ErrTargetClosed) {
		t.Fatalf("a shutdown reported as %v", err)
	}
	if agents, _, archived, wsArchived := b2.counts(); agents != 1 || archived != 0 || wsArchived != 0 {
		t.Fatalf("a shutdown archived the agent or its workspace (live=%d archived=%d ws=%d)", agents, archived, wsArchived)
	}

	// Not bound to its target: no label.
	b3 := newDaemonBackend()
	d3 := newResumeDispatcher(t, b3)
	var starts []AgentStart
	if _, err := d3.Dispatch(context.Background(), resumeStep(&starts)); err != nil {
		t.Fatal(err)
	}
	for _, l := range argAfter(b3.runs[0], "--label") {
		if strings.HasPrefix(l, boundLabel+"=") {
			t.Fatalf("an unbound launch carries a %s label", boundLabel)
		}
	}
}
