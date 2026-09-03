package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubSession is a minimal Session that records Close and can signal Wait.
type stubSession struct {
	id       string
	closed   bool
	waitDone chan struct{}
}

func (s *stubSession) ID() string { return s.id }
func (s *stubSession) Prompt(context.Context, Message) (<-chan Update, error) {
	ch := make(chan Update)
	close(ch)
	return ch, nil
}
func (s *stubSession) Cancel(context.Context) error { return nil }
func (s *stubSession) Close(context.Context) error  { s.closed = true; return nil }
func (s *stubSession) Wait(context.Context, time.Duration) {
	if s.waitDone != nil {
		<-s.waitDone
	}
}

// stubController hands out stubSessions and records the spec it received.
type fakeSessCtl struct {
	transport Transport
	sess      *stubSession
	err       error
	gotSpec   Spec
}

func (c *fakeSessCtl) Name() string         { return "fake" }
func (c *fakeSessCtl) Model() SessionModel  { return ModelResumable }
func (c *fakeSessCtl) Transport() Transport { return c.transport }
func (c *fakeSessCtl) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{Transport: c.transport}, nil
}
func (c *fakeSessCtl) NewSession(_ context.Context, spec Spec, _ Handler) (Session, error) {
	c.gotSpec = spec
	if c.err != nil {
		return nil, c.err
	}
	return c.sess, nil
}
func (c *fakeSessCtl) ResumeSession(context.Context, string, Handler) (Session, error) {
	return c.sess, nil
}
func (c *fakeSessCtl) Runner() (Runner, error) { return nil, nil }

func TestControllerRunnerDispatchProvisionsAndOpens(t *testing.T) {
	sess := &stubSession{id: "sess-1"}
	ctl := &fakeSessCtl{transport: TransportACP, sess: sess}
	prov := &fakeProv{id: "ws-1", cwd: "/wt/o-r-7"}
	r := newControllerRunner(ctl, prov, nil)

	req := makeReq("merge_conflict", "fix it")
	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if ref.AgentID != "sess-1" {
		t.Fatalf("RunRef agent id = %q, want sess-1", ref.AgentID)
	}
	if ref.Backend != string(TransportACP) {
		t.Fatalf("RunRef backend = %q, want acp", ref.Backend)
	}
	if len(prov.got) != 1 {
		t.Fatalf("provisioner called %d times, want 1", len(prov.got))
	}
	// The conductor-provisioned worktree reaches the controller as the session cwd.
	if ctl.gotSpec.Cwd != "/wt/o-r-7" || ctl.gotSpec.WorkspaceID != "ws-1" {
		t.Fatalf("spec cwd/ws = %q/%q, want /wt/o-r-7 / ws-1", ctl.gotSpec.Cwd, ctl.gotSpec.WorkspaceID)
	}
	// The dedup gate sees the live session; Archive clears it and closes the session.
	if !r.HasLiveAgent(context.Background(), req.Trigger.Key(), req.Trigger.Kind) {
		t.Fatal("HasLiveAgent should report the open session")
	}
	if err := r.Archive(context.Background(), "sess-1"); err != nil {
		t.Fatal(err)
	}
	if !sess.closed {
		t.Fatal("Archive must close the session")
	}
	if r.HasLiveAgent(context.Background(), req.Trigger.Key(), req.Trigger.Kind) {
		t.Fatal("HasLiveAgent should clear after Archive")
	}
}

func TestControllerRunnerShadowSkipsLaunch(t *testing.T) {
	ctl := &fakeSessCtl{transport: TransportCLI, sess: &stubSession{id: "x"}}
	prov := &fakeProv{}
	r := newControllerRunner(ctl, prov, nil)

	req := makeReq("merge_conflict", "fix")
	req.Shadow = true
	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Shadowed || ref.AgentID != "" {
		t.Fatalf("shadow dispatch must not launch, got %+v", ref)
	}
	if len(prov.got) != 0 {
		t.Fatal("shadow dispatch must not provision a worktree")
	}
}

func TestControllerRunnerWaitClearsLiveness(t *testing.T) {
	release := make(chan struct{})
	sess := &stubSession{id: "s", waitDone: release}
	ctl := &fakeSessCtl{transport: TransportACP, sess: sess}
	r := newControllerRunner(ctl, &fakeProv{}, nil)

	req := makeReq("review_requested", "review")
	if _, err := r.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { r.WaitForAgent(context.Background(), "s", 0); close(done) }()
	// Still waiting until the session signals completion.
	select {
	case <-done:
		t.Fatal("WaitForAgent returned before the session finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForAgent did not return after completion")
	}
	if r.HasLiveAgent(context.Background(), req.Trigger.Key(), req.Trigger.Kind) {
		t.Fatal("liveness should clear once the agent finishes")
	}
}

func TestControllerRunnerLivenessIsPerPR(t *testing.T) {
	ctl := &fakeSessCtl{transport: TransportACP}
	r := newControllerRunner(ctl, &fakeProv{}, nil)

	// Two sessions on two different PRs.
	a := makeReq("review_requested", "a")
	a.Trigger.Target.PR, a.Trigger.Target.Number = 1, 1
	b := makeReq("review_requested", "b")
	b.Trigger.Target.PR, b.Trigger.Target.Number = 2, 2

	ctl.sess = &stubSession{id: "s1"}
	if _, err := r.Dispatch(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	ctl.sess = &stubSession{id: "s2"}
	if _, err := r.Dispatch(context.Background(), b); err != nil {
		t.Fatal(err)
	}

	// Closing PR #1's session must not clear PR #2's liveness.
	if err := r.Archive(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if r.HasLiveAgent(context.Background(), a.Trigger.Key(), a.Trigger.Kind) {
		t.Fatal("PR #1 liveness should clear")
	}
	if !r.HasLiveAgent(context.Background(), b.Trigger.Key(), b.Trigger.Kind) {
		t.Fatal("PR #2 liveness must remain after closing PR #1")
	}
}

func TestControllerRunnerDispatchPropagatesProvisionError(t *testing.T) {
	ctl := &fakeSessCtl{transport: TransportACP, sess: &stubSession{id: "x"}}
	r := newControllerRunner(ctl, &fakeProv{err: errors.New("no checkout")}, nil)
	if _, err := r.Dispatch(context.Background(), makeReq("merge_conflict", "x")); err == nil {
		t.Fatal("a provisioning failure must surface as a dispatch error")
	}
}

func TestResolvePermissionAutoApprove(t *testing.T) {
	// No handler → auto-approve, preferring a one-shot allow option.
	out, err := resolvePermission(context.Background(), nil, PermissionRequest{
		Options: []string{"Allow once", "Allow always", "Reject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Approved || out.Selected != "Allow once" {
		t.Fatalf("auto-approve = %+v, want approved with 'Allow once'", out)
	}

	// No allow-shaped option → bare approval.
	out2, _ := resolvePermission(context.Background(), nil, PermissionRequest{Options: []string{"Proceed"}})
	if !out2.Approved || out2.Selected != "" {
		t.Fatalf("bare approval = %+v", out2)
	}
}

// handlerFunc adapts a function to the Handler interface (permission only).
type handlerFunc struct {
	perm func(PermissionRequest) (PermissionOutcome, error)
}

func (h handlerFunc) RequestPermission(_ context.Context, req PermissionRequest) (PermissionOutcome, error) {
	return h.perm(req)
}
func (h handlerFunc) RequestInput(context.Context, InputRequest) (InputOutcome, error) {
	return InputOutcome{}, nil
}

func TestResolvePermissionDelegatesToHandler(t *testing.T) {
	h := handlerFunc{perm: func(PermissionRequest) (PermissionOutcome, error) {
		return PermissionOutcome{Selected: "Reject", Approved: false}, nil
	}}
	out, err := resolvePermission(context.Background(), h, PermissionRequest{Options: []string{"Allow once", "Reject"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Approved {
		t.Fatalf("handler denial must be honored, got %+v", out)
	}
}
