package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

// cancellableSession is a Session whose Cancel ends the in-flight Wait — a more
// faithful fake than stubSession for exercising CancelTarget racing a
// foreground Dispatch: a real controller's Cancel kills the underlying process,
// which is exactly what unblocks Wait.
type cancellableSession struct {
	id        string
	waitDone  chan struct{}
	cancelled bool
	closed    bool
}

func (s *cancellableSession) ID() string { return s.id }
func (s *cancellableSession) Prompt(context.Context, Message) (<-chan Update, error) {
	ch := make(chan Update)
	close(ch)
	return ch, nil
}
func (s *cancellableSession) Cancel(context.Context) error {
	s.cancelled = true
	select {
	case <-s.waitDone:
	default:
		close(s.waitDone)
	}
	return nil
}
func (s *cancellableSession) Close(context.Context) error { s.closed = true; return nil }
func (s *cancellableSession) Wait(context.Context, time.Duration) {
	<-s.waitDone
}

// cancellableSessCtl hands out a single cancellableSession — fakeSessCtl's
// sess field is typed *stubSession, so this test uses its own minimal
// Controller fake instead.
type cancellableSessCtl struct {
	transport Transport
	sess      *cancellableSession
}

func (c *cancellableSessCtl) Name() string         { return "fake" }
func (c *cancellableSessCtl) Model() SessionModel  { return ModelResumable }
func (c *cancellableSessCtl) Transport() Transport { return c.transport }
func (c *cancellableSessCtl) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{Transport: c.transport}, nil
}
func (c *cancellableSessCtl) NewSession(context.Context, Spec, Handler) (Session, error) {
	return c.sess, nil
}
func (c *cancellableSessCtl) ResumeSession(context.Context, string, bool, Handler) (Session, error) {
	return c.sess, nil
}
func (c *cancellableSessCtl) Runner() (Runner, error) { return nil, nil }

func TestControllerRunnerCancelTargetInterruptsForegroundDispatch(t *testing.T) {
	sess := &cancellableSession{id: "s1", waitDone: make(chan struct{})}
	ctl := &cancellableSessCtl{transport: TransportCLI, sess: sess}
	r := newControllerRunner(ctl, &fakeProv{}, nil)

	req := makeReq("merge_conflict", "fix it")
	req.Wait = true
	prKey := req.Trigger.Key()

	done := make(chan struct{})
	var dispatchErr error
	go func() {
		_, dispatchErr = r.Dispatch(context.Background(), req)
		close(done)
	}()

	// Give Dispatch time to register the session before cancelling it.
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		_, live := r.live["s1"]
		r.mu.Unlock()
		if live {
			break
		}
		select {
		case <-deadline:
			t.Fatal("session never registered as live")
		case <-time.After(time.Millisecond):
		}
	}

	ids := r.CancelTarget(context.Background(), prKey, "target merged")
	if len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("CancelTarget ids = %v, want [s1]", ids)
	}
	if !sess.cancelled {
		t.Fatal("CancelTarget must call Session.Cancel")
	}
	if !sess.closed {
		t.Fatal("CancelTarget must call Session.Close")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch did not return after CancelTarget")
	}
	if dispatchErr == nil {
		t.Fatal("Dispatch should report an error after its target was cancelled")
	}
	if !errors.Is(dispatchErr, ErrTargetClosed) {
		t.Fatalf("Dispatch error = %v, want ErrTargetClosed", dispatchErr)
	}
	if r.HasLiveAgent(context.Background(), req.Trigger.Key(), req.Trigger.Kind) {
		t.Fatal("liveness should clear once CancelTarget forgets the session")
	}
}

func TestControllerRunnerCancelTargetOnlyTouchesItsOwnTarget(t *testing.T) {
	ctl := &fakeSessCtl{transport: TransportACP}
	r := newControllerRunner(ctl, &fakeProv{}, nil)

	a := makeReq("review_requested", "a")
	a.Trigger.Target.Repo = "o/r"
	a.Trigger.Target.PR, a.Trigger.Target.Number = 1, 1
	b := makeReq("review_requested", "b")
	b.Trigger.Target.Repo = "o/r"
	b.Trigger.Target.PR, b.Trigger.Target.Number = 2, 2

	sessA := &stubSession{id: "sa"}
	ctl.sess = sessA
	if _, err := r.Dispatch(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	sessB := &stubSession{id: "sb"}
	ctl.sess = sessB
	if _, err := r.Dispatch(context.Background(), b); err != nil {
		t.Fatal(err)
	}

	ids := r.CancelTarget(context.Background(), a.Trigger.Key(), "target closed")
	if len(ids) != 1 || ids[0] != "sa" {
		t.Fatalf("CancelTarget ids = %v, want [sa]", ids)
	}
	if !sessA.closed {
		t.Fatal("target a's session should be closed")
	}
	if sessB.closed {
		t.Fatal("target b's session must be untouched")
	}
	if !r.HasLiveAgent(context.Background(), b.Trigger.Key(), b.Trigger.Kind) {
		t.Fatal("target b's liveness must remain")
	}
}

func TestControllerRunnerCancelTargetNoLiveAgentsIsNoop(t *testing.T) {
	ctl := &fakeSessCtl{transport: TransportACP}
	r := newControllerRunner(ctl, &fakeProv{}, nil)
	ids := r.CancelTarget(context.Background(), "o/r#9", "target merged")
	if len(ids) != 0 {
		t.Fatalf("CancelTarget on an idle target should cancel nothing, got %v", ids)
	}
}
