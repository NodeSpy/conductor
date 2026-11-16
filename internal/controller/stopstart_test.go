package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// slowOpenCtl is cancelCtl whose session takes until release to open: a
// dispatch caught between provisioning and a tracked session.
type slowOpenCtl struct {
	cancelCtl
	opening chan struct{}
	release chan struct{}
}

func (c *slowOpenCtl) NewSession(ctx context.Context, s Spec, h Handler) (Session, error) {
	close(c.opening)
	<-c.release
	return c.cancelCtl.NewSession(ctx, s, h)
}

// A PR that closes while its fixer is still starting (no session to cancel
// yet) stops that fixer too: the moment its session is tracked the turn is
// cancelled and Dispatch reports ErrTargetClosed — no window where a
// just-launched agent escapes the stop.
func TestStopTargetCatchesAStartingFixer(t *testing.T) {
	sess := newCancelSession("fix-1")
	c := &slowOpenCtl{cancelCtl: cancelCtl{sess: sess}, opening: make(chan struct{}), release: make(chan struct{})}
	r := newControllerRunner(c, &fakeProv{}, nil)
	req := makeReq("new_comment", "address it")
	req.Wait = true
	errc := make(chan error, 1)
	go func() {
		_, err := r.Dispatch(context.Background(), req)
		errc <- err
	}()
	select {
	case <-c.opening:
	case <-time.After(2 * time.Second):
		t.Fatal("the session never started opening")
	}
	if n := r.StopTarget(context.Background(), "o/other#9"); n != 0 {
		t.Fatalf("stopped %d fixers on another PR", n)
	}
	if n := r.StopTarget(context.Background(), req.Trigger.Key()); n != 1 {
		t.Fatalf("StopTarget = %d, want the starting fixer", n)
	}
	close(c.release)
	select {
	case err := <-errc:
		if !errors.Is(err, dispatch.ErrTargetClosed) {
			t.Fatalf("Dispatch err = %v, want ErrTargetClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the starting fixer escaped the stop")
	}
	select {
	case <-sess.cancelled:
	default:
		t.Fatal("the starting fixer's turn was not cancelled")
	}
}
