package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// recordRunner records dispatched requests and returns a canned result — so a
// test can prove the paseo controller passes requests through unmodified.
type recordRunner struct {
	reqs []dispatch.Request
	ref  dispatch.RunRef
	err  error
}

func (r *recordRunner) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	r.reqs = append(r.reqs, req)
	return r.ref, r.err
}
func (r *recordRunner) WaitForAgent(context.Context, string, time.Duration) {}
func (r *recordRunner) HasLiveAgent(context.Context, string, string) bool   { return false }
func (r *recordRunner) Archive(context.Context, string) error               { return nil }

type recordSender struct{ sent []string }

func (s *recordSender) Send(_ context.Context, id, prompt string) error {
	s.sent = append(s.sent, id+":"+prompt)
	return nil
}

func TestResolveDefaultsToBuiltinPaseo(t *testing.T) {
	run := &recordRunner{}
	reg := NewRegistry(nil, "", run, nil)
	c, err := reg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != BuiltinPaseo {
		t.Fatalf("want built-in paseo, got %q", c.Name())
	}
	if c.Model() != ModelNative || c.Transport() != TransportNative {
		t.Fatalf("paseo must be native/native, got %q/%q", c.Model(), c.Transport())
	}
	r, err := reg.RunnerFor("")
	if err != nil {
		t.Fatal(err)
	}
	if r != Runner(run) {
		t.Fatal("built-in paseo must run through the injected dispatcher unchanged")
	}
}

func TestResolveExplicitWinsAndDefaultFlag(t *testing.T) {
	run := &recordRunner{}
	cfgs := map[string]config.ControllerConfig{
		"pae": {Type: "paseo", Default: true},
		"gem": {Agent: "gemini"},
	}
	reg := NewRegistry(cfgs, "pae", run, nil)

	// An explicit per-agent controller wins over the default flag.
	c, err := reg.Resolve("gem")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != "gem" {
		t.Fatalf("explicit controller should win, got %q", c.Name())
	}
	if _, err := c.Runner(); err != ErrNotRunnable {
		t.Fatalf("gemini stub must not be runnable in this build, got %v", err)
	}

	// No explicit controller → the default:true controller (a paseo type → runnable).
	c2, err := reg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Name() != "pae" {
		t.Fatalf("default:true should resolve, got %q", c2.Name())
	}
	if _, err := c2.Runner(); err != nil {
		t.Fatalf("a paseo-type default must be runnable, got %v", err)
	}
}

func TestResolveUnknownController(t *testing.T) {
	reg := NewRegistry(nil, "", &recordRunner{}, nil)
	if _, err := reg.Resolve("nope"); err == nil {
		t.Fatal("an unknown controller name must error")
	}
}

func TestPaseoCapabilities(t *testing.T) {
	reg := NewRegistry(nil, "", &recordRunner{}, &recordSender{})
	c, _ := reg.Resolve("")
	caps, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.SessionModel != ModelNative || caps.Transport != TransportNative {
		t.Fatalf("bad negotiated caps: %+v", caps)
	}
	if !caps.CheckoutPR || !caps.InteractiveHandoff || !caps.SendFollowup {
		t.Fatalf("paseo capabilities missing: %+v", caps)
	}
}

func TestPaseoNewSessionPassesRequestVerbatim(t *testing.T) {
	run := &recordRunner{ref: dispatch.RunRef{AgentID: "ag_1"}}
	reg := NewRegistry(nil, "", run, nil)
	c, _ := reg.Resolve("")
	req := dispatch.Request{
		Trigger: core.Trigger{Kind: "merge_conflict"},
		Action:  config.Action{Type: "agent", Prompt: "fix"},
	}
	sess, err := c.NewSession(context.Background(), Spec{Request: req}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID() != "ag_1" {
		t.Fatalf("session id should come from the launch ref, got %q", sess.ID())
	}
	if len(run.reqs) != 1 {
		t.Fatalf("dispatch should be called once, got %d", len(run.reqs))
	}
	// The controller adds nothing: the request reaches the dispatcher unmodified.
	if run.reqs[0].Action.Prompt != "fix" || run.reqs[0].Trigger.Kind != "merge_conflict" {
		t.Fatalf("request must pass through unmodified, got %+v", run.reqs[0])
	}
}

func TestPaseoSessionPromptSends(t *testing.T) {
	snd := &recordSender{}
	reg := NewRegistry(nil, "", &recordRunner{}, snd)
	c, _ := reg.Resolve("")
	sess, _ := c.ResumeSession(context.Background(), "ag_9", nil)
	ch, err := sess.Prompt(context.Background(), Message{Text: "more"})
	if err != nil {
		t.Fatal(err)
	}
	up := <-ch
	if up.Kind != UpdateDone || up.AgentID != "ag_9" {
		t.Fatalf("expected a terminal update for ag_9, got %+v", up)
	}
	if len(snd.sent) != 1 || snd.sent[0] != "ag_9:more" {
		t.Fatalf("follow-up not delivered via send: %v", snd.sent)
	}
}

func TestPaseoPromptWithoutSender(t *testing.T) {
	reg := NewRegistry(nil, "", &recordRunner{}, nil)
	c, _ := reg.Resolve("")
	sess, _ := c.ResumeSession(context.Background(), "ag_9", nil)
	if _, err := sess.Prompt(context.Background(), Message{Text: "x"}); err != ErrNoFollowup {
		t.Fatalf("a controller without a sender must report ErrNoFollowup, got %v", err)
	}
}

// TestBuiltinRunnerIsRealDispatcher proves resolving to the built-in paseo
// controller yields the genuine paseo CLI argv — i.e. controller selection is
// behavior-preserving for today's (no `controllers:` block) configs.
func TestBuiltinRunnerIsRealDispatcher(t *testing.T) {
	disp := dispatch.New("paseo", config.Retry{}, true) // dry-run: no real exec
	reg := NewRegistry(nil, "", disp, disp)
	r, err := reg.RunnerFor("")
	if err != nil {
		t.Fatal(err)
	}
	req := dispatch.Request{
		Trigger: core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "a/w", PR: 5, Number: 5}},
		Action:  config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"},
		Profile: config.AgentProfile{Workspace: "worktree"},
	}
	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(ref.Argv, " ")
	if !strings.Contains(s, "paseo run") || !strings.Contains(s, "--worktree-mode checkout-pr") {
		t.Fatalf("built-in paseo must produce the same paseo argv, got: %s", s)
	}
}

func TestSessionModelAndTransportValid(t *testing.T) {
	for _, m := range []SessionModel{ModelNative, ModelResumable, ModelOneshot} {
		if !m.Valid() {
			t.Errorf("%q should be a valid session model", m)
		}
	}
	if SessionModel("bogus").Valid() {
		t.Error("bogus session model must be invalid")
	}
	for _, tr := range []Transport{TransportACP, TransportNative, TransportCLI} {
		if !tr.Valid() {
			t.Errorf("%q should be a valid transport", tr)
		}
	}
	if Transport("bogus").Valid() {
		t.Error("bogus transport must be invalid")
	}
}
