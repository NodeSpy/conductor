package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// assertNotLive fails when the runner still reports the request's session as
// live: listed by LiveAgents (`conductor status`) or holding the PR+kind gate.
func assertNotLive(t *testing.T, r *controllerRunner, req dispatch.Request) {
	t.Helper()
	if live := r.LiveAgents(); len(live) != 0 {
		t.Fatalf("the dispatch returned but %d agent(s) still listed live: %+v", len(live), live)
	}
	if r.HasLiveAgent(context.Background(), req.Trigger.Key(), req.Trigger.Kind) {
		t.Fatal("the dispatch returned but its PR+kind still gates as live")
	}
}

// A foreground step whose reply fails the output_schema contract even after
// the corrective turn fails, and the flow keeps its agent (a failed step does
// not archive). Its turn is over: the session must not be listed live — the
// stale `claude-code-2 … (running 2h37m)` row an hour after the run failed —
// nor block the PR's next event. Archive must still close it and release
// the workspace it owns.
func TestForegroundSchemaFailureIsNotLeftLive(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}}
	l := &fakeLauncher{out: func([]string) (string, error) {
		return `{"result":"no structured answer here"}`, nil
	}}
	prov := &fakeProv{id: "ws-1", cwd: t.TempDir()}
	c := newCLIController("claude-code", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, prov)
	c.launch = l.launch
	r := newControllerRunner(c, prov, nil)

	req := makeReq("changes_requested", "address it")
	req.Wait = true
	req.Action.OutputSchema = schema
	req.Step.OutputSchema = schema

	ref, err := r.Dispatch(context.Background(), req)
	if err == nil {
		t.Fatal("want the output_schema failure")
	}
	if l.count() != 2 {
		t.Fatalf("want the turn plus one corrective turn, got %d launches", l.count())
	}
	assertNotLive(t, r, req)

	if err := r.Archive(context.Background(), ref.AgentID); err != nil {
		t.Fatal(err)
	}
	if len(prov.removed) != 1 || prov.removed[0] != "ws-1" {
		t.Fatalf("Archive of the settled session must still release its workspace; removed %v", prov.removed)
	}
	if len(r.live) != 0 || len(r.byPR) != 0 {
		t.Fatalf("Archive left state behind: live=%v byPR=%v", r.live, r.byPR)
	}
}

// Every other way a foreground dispatch ends leaves nothing live either.
func TestForegroundDispatchExitPathsAreNotLeftLive(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := newControllerRunner(&fakeSessCtl{transport: TransportCLI, sess: &stubSession{id: "s1"}}, &fakeProv{}, nil)
		req := makeReq("changes_requested", "fix")
		req.Wait = true
		if _, err := r.Dispatch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		assertNotLive(t, r, req)
	})
	t.Run("turn error", func(t *testing.T) {
		l := &fakeLauncher{out: func([]string) (string, error) {
			return `{"type":"result","is_error":true,"result":"API Error: overloaded"}`, nil
		}}
		c := newCLIController("claude-code", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
		c.launch = l.launch
		r := newControllerRunner(c, nil, nil)
		req := makeReq("changes_requested", "fix")
		req.Wait = true
		if _, err := r.Dispatch(context.Background(), req); err == nil {
			t.Fatal("want the turn error")
		}
		assertNotLive(t, r, req)
	})
	t.Run("stopped", func(t *testing.T) {
		sess := newCancelSession("fix-1")
		r := newControllerRunner(&cancelCtl{sess: sess}, &fakeProv{}, nil)
		req := makeReq("changes_requested", "fix")
		req.Wait = true
		done := make(chan error, 1)
		go func() { _, err := r.Dispatch(context.Background(), req); done <- err }()
		for len(r.LiveAgents()) == 0 {
			time.Sleep(time.Millisecond)
		}
		r.mu.Lock()
		r.stopped["fix-1"] = true
		r.mu.Unlock()
		_ = sess.Cancel(context.Background())
		if err := <-done; !errors.Is(err, dispatch.ErrTargetClosed) {
			t.Fatalf("want ErrTargetClosed, got %v", err)
		}
		assertNotLive(t, r, req)
	})
	t.Run("interrupted", func(t *testing.T) {
		sess := newCancelSession("fix-1")
		r := newControllerRunner(&cancelCtl{sess: sess}, &fakeProv{}, nil)
		req := makeReq("changes_requested", "fix")
		req.Wait = true
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := r.Dispatch(ctx, req); done <- err }()
		for len(r.LiveAgents()) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
		if err := <-done; err == nil {
			t.Fatal("want the interruption")
		}
		assertNotLive(t, r, req)
	})
	t.Run("resume fallback", func(t *testing.T) {
		// The resumed session could not be found: a fresh agent starts in the
		// kept workspace (the live run: resumed after a restart, then failed).
		ctl := &resumeCtl{canResume: true, turnErr: errors.New("no conversation found")}
		prov := &adoptProv{fakeProv: fakeProv{id: "new", cwd: "/new"}}
		var starts []dispatch.AgentStart
		req := resumeReq(&starts)
		r := newControllerRunner(ctl, prov, nil)
		if _, err := r.Dispatch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if len(ctl.fresh) != 1 {
			t.Fatalf("want the fresh fallback agent, got %d", len(ctl.fresh))
		}
		assertNotLive(t, r, req)
	})
}

// A background hand-off is the session you drive: it stays live after
// Dispatch returns, until it is archived.
func TestInteractiveDispatchStaysLive(t *testing.T) {
	r := newControllerRunner(&fakeSessCtl{transport: TransportCLI, sess: &stubSession{id: "h1"}}, &fakeProv{}, nil)
	req := makeReq("review_requested", "review")
	req.Interactive = true
	if _, err := r.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(r.LiveAgents()) != 1 || !r.HasLiveAgent(context.Background(), req.Trigger.Key(), req.Trigger.Kind) {
		t.Fatal("a hand-off session must stay live after Dispatch returns")
	}
	if err := r.Archive(context.Background(), "h1"); err != nil {
		t.Fatal(err)
	}
	assertNotLive(t, r, req)
}
