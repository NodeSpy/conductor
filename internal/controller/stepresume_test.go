package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// resumeCtl is a synthetic runtime that can (or cannot) resume a step.
type resumeCtl struct {
	canResume bool
	resumeErr error // ResumeStep fails with it
	turnErr   error // the resumed session's ResumeErr

	mu      sync.Mutex
	fresh   []Spec
	resumed []Spec
	ids     []string // the session ids ResumeStep was asked for
	n       int
}

func (c *resumeCtl) Name() string         { return "rt" }
func (c *resumeCtl) Model() SessionModel  { return ModelResumable }
func (c *resumeCtl) Transport() Transport { return TransportCLI }
func (c *resumeCtl) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{ResumeStep: c.canResume}, nil
}
func (c *resumeCtl) ResumeSession(context.Context, string, bool, Handler) (Session, error) {
	return nil, ErrNoFollowup
}
func (c *resumeCtl) Runner() (Runner, error) { return nil, nil }
func (c *resumeCtl) CanResumeStep() bool     { return c.canResume }

func (c *resumeCtl) NewSession(_ context.Context, spec Spec, _ Handler) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fresh = append(c.fresh, spec)
	c.n++
	return &resumeSess{id: "s" + string(rune('0'+c.n)), tool: "tool-" + string(rune('0'+c.n))}, nil
}

func (c *resumeCtl) ResumeStep(_ context.Context, spec Spec, id string, _ Handler) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resumeErr != nil {
		return nil, c.resumeErr
	}
	c.resumed = append(c.resumed, spec)
	c.ids = append(c.ids, id)
	c.n++
	return &resumeSess{id: "s" + string(rune('0'+c.n)), tool: id, failed: c.turnErr}, nil
}

type resumeSess struct {
	id, tool string
	failed   error
}

func (s *resumeSess) ID() string { return s.id }
func (s *resumeSess) Prompt(context.Context, Message) (<-chan Update, error) {
	return nil, ErrNoFollowup
}
func (s *resumeSess) Cancel(context.Context) error        { return nil }
func (s *resumeSess) Close(context.Context) error         { return nil }
func (s *resumeSess) Wait(context.Context, time.Duration) {}
func (s *resumeSess) ResumeID() string                    { return s.tool }
func (s *resumeSess) ResumeErr() error                    { return s.failed }
func (s *resumeSess) Output() string                      { return "ok" }
func (s *resumeSess) TurnErr() error                      { return nil }
func (s *resumeSess) String() string                      { return s.id }

// adoptProv is a provisioner that can (or cannot) take a kept workspace back.
type adoptProv struct {
	fakeProv
	adoptErr error
	adopted  []string
}

func (p *adoptProv) AdoptWorktree(_ context.Context, _ dispatch.Request, id string) (string, error) {
	if p.adoptErr != nil {
		return "", p.adoptErr
	}
	p.adopted = append(p.adopted, id)
	return "/kept/" + id, nil
}

// resumeReq is a foreground step resuming the agent recorded on runtime "rt".
func resumeReq(starts *[]dispatch.AgentStart) dispatch.Request {
	req := makeReq("pr_review", "fix the build")
	req.Wait = true
	req.Resume = &dispatch.AgentSession{Runtime: "rt", SessionID: "sess-1", Workspace: "ws-1", Cwd: "/kept/ws-1", CanResume: true}
	req.OnAgentStart = func(s dispatch.AgentStart) { *starts = append(*starts, s) }
	return req
}

func TestStepResumeContinuesSessionInKeptWorkspace(t *testing.T) {
	ctl := &resumeCtl{canResume: true}
	prov := &adoptProv{fakeProv: fakeProv{id: "new", cwd: "/new"}}
	var starts []dispatch.AgentStart
	if _, err := newControllerRunner(ctl, prov, nil).Dispatch(context.Background(), resumeReq(&starts)); err != nil {
		t.Fatal(err)
	}
	if len(prov.got) != 0 || len(prov.adopted) != 1 || prov.adopted[0] != "ws-1" {
		t.Fatalf("want the kept workspace adopted and nothing provisioned; adopted=%v provisioned=%d", prov.adopted, len(prov.got))
	}
	if len(ctl.resumed) != 1 || len(ctl.fresh) != 0 || ctl.ids[0] != "sess-1" {
		t.Fatalf("want one resume of sess-1, no fresh agent; resumed=%v fresh=%d", ctl.ids, len(ctl.fresh))
	}
	sp := ctl.resumed[0]
	if sp.Cwd != "/kept/ws-1" || sp.WorkspaceID != "ws-1" {
		t.Fatalf("resumed in %q (%q), want the kept workspace", sp.Cwd, sp.WorkspaceID)
	}
	if sp.Request.Action.Prompt != dispatch.ResumeNote || !sp.Request.LiteralPrompt {
		t.Fatalf("a resumed session gets the restart note as its turn, got %q", sp.Request.Action.Prompt)
	}
	if len(starts) != 1 || !starts[0].Resumed || !starts[0].KeptWorkspace || starts[0].SessionID != "sess-1" ||
		starts[0].Workspace != "ws-1" || !starts[0].CanResume || starts[0].Runtime != "rt" {
		t.Fatalf("start report = %+v", starts)
	}
}

func TestStepResumeFallsBackToFreshAgentInKeptWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctl  *resumeCtl
		want string // in the fallback reason
		mod  func(*dispatch.Request)
	}{
		{"runtime cannot resume", &resumeCtl{}, "cannot resume", nil},
		{"resume fails to start", &resumeCtl{canResume: true, resumeErr: errors.New("boom")}, "resume failed: boom", nil},
		{"resumed session not found", &resumeCtl{canResume: true, turnErr: errors.New("no conversation")}, "resumed session failed: no conversation", nil},
		{"no session recorded", &resumeCtl{canResume: true}, "no session", func(r *dispatch.Request) { r.Resume.SessionID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &adoptProv{fakeProv: fakeProv{id: "new", cwd: "/new"}}
			var starts []dispatch.AgentStart
			req := resumeReq(&starts)
			if tc.mod != nil {
				tc.mod(&req)
			}
			ref, err := newControllerRunner(tc.ctl, prov, nil).Dispatch(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.ctl.fresh) != 1 {
				t.Fatalf("want one fresh agent, got %d", len(tc.ctl.fresh))
			}
			sp := tc.ctl.fresh[0]
			if sp.Cwd != "/kept/ws-1" || len(prov.got) != 0 {
				t.Fatalf("fresh agent in %q (provisioned %d), want the kept workspace", sp.Cwd, len(prov.got))
			}
			if !strings.HasPrefix(sp.Request.Action.Prompt, dispatch.RestartNote) || !strings.HasSuffix(sp.Request.Action.Prompt, "fix the build") {
				t.Fatalf("fresh prompt = %q, want the restart note then the step's prompt", sp.Request.Action.Prompt)
			}
			last := starts[len(starts)-1]
			if last.Resumed || !last.KeptWorkspace || !strings.Contains(last.Fallback, tc.want) {
				t.Fatalf("start report = %+v, want a fallback mentioning %q", last, tc.want)
			}
			if ref.AgentID == "" {
				t.Fatal("no agent id")
			}
		})
	}
}

func TestStepResumeWorkspaceGoneStartsFreshEverywhere(t *testing.T) {
	ctl := &resumeCtl{canResume: true}
	prov := &adoptProv{fakeProv: fakeProv{id: "new", cwd: "/new"}, adoptErr: errors.New("worktree ws-1 is gone")}
	var starts []dispatch.AgentStart
	if _, err := newControllerRunner(ctl, prov, nil).Dispatch(context.Background(), resumeReq(&starts)); err != nil {
		t.Fatal(err)
	}
	if len(ctl.resumed) != 0 || len(ctl.fresh) != 1 || ctl.fresh[0].Cwd != "/new" {
		t.Fatalf("want a fresh agent in a freshly provisioned workspace; resumed=%d fresh=%+v", len(ctl.resumed), ctl.fresh)
	}
	if strings.HasPrefix(ctl.fresh[0].Request.Action.Prompt, dispatch.RestartNote) {
		t.Fatal("a fresh workspace holds no earlier work: no restart note")
	}
	if len(starts) != 1 || starts[0].KeptWorkspace || !strings.Contains(starts[0].Fallback, "gone") {
		t.Fatalf("start report = %+v", starts)
	}
}

func TestStepResumeOtherRuntimeNeverReusesTheSession(t *testing.T) {
	ctl := &resumeCtl{canResume: true}
	prov := &adoptProv{fakeProv: fakeProv{id: "new", cwd: "/new"}}
	var starts []dispatch.AgentStart
	req := resumeReq(&starts)
	req.Resume.Runtime = "elsewhere"
	if _, err := newControllerRunner(ctl, prov, nil).Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(ctl.resumed) != 0 || len(prov.adopted) != 0 || len(prov.got) != 1 {
		t.Fatalf("another runtime's agent must not be resumed or its workspace adopted; resumed=%d adopted=%v", len(ctl.resumed), prov.adopted)
	}
}

func TestStepStartReportsSessionForLaterResume(t *testing.T) {
	ctl := &resumeCtl{canResume: true}
	prov := &adoptProv{fakeProv: fakeProv{id: "ws-9", cwd: "/wt/9"}}
	var starts []dispatch.AgentStart
	req := makeReq("pr_review", "p")
	req.Wait = true
	req.OnAgentStart = func(s dispatch.AgentStart) { starts = append(starts, s) }
	if _, err := newControllerRunner(ctl, prov, nil).Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(starts) != 1 {
		t.Fatalf("starts = %+v", starts)
	}
	s := starts[0]
	if s.Resumed || s.Fallback != "" || s.SessionID != "tool-1" || s.Workspace != "ws-9" || s.Cwd != "/wt/9" || !s.CanResume || s.Runtime != "rt" {
		t.Fatalf("start report = %+v", s)
	}
}

func TestStepResumeInterruptedKeepsWorkspace(t *testing.T) {
	ctl := &resumeCtl{canResume: true, resumeErr: errors.New("boom")}
	ctlFail := &failNewCtl{resumeCtl: ctl}
	prov := &adoptProv{fakeProv: fakeProv{id: "new", cwd: "/new"}}
	var starts []dispatch.AgentStart
	if _, err := newControllerRunner(ctlFail, prov, nil).Dispatch(context.Background(), resumeReq(&starts)); err == nil {
		t.Fatal("want the launch error")
	}
	if len(prov.removed) != 0 {
		t.Fatalf("a kept workspace must survive a failed launch: removed %v", prov.removed)
	}
}

// failNewCtl is a resumeCtl whose fresh launches fail.
type failNewCtl struct{ *resumeCtl }

func (c *failNewCtl) NewSession(context.Context, Spec, Handler) (Session, error) {
	return nil, errors.New("launch failed")
}

// ---- the cli runtime ---------------------------------------------------------

func TestCLIClaudeNamesSessionForTrackedStep(t *testing.T) {
	l := &fakeLauncher{out: func([]string) (string, error) { return `{"session_id":"x","result":"ok"}`, nil }}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	if !c.CanResumeStep() {
		t.Fatal("the claude-code recipe resumes steps")
	}
	req := makeReq("pr_review", "p")
	req.OnAgentStart = func(dispatch.AgentStart) {}
	s, err := c.NewSession(context.Background(), Spec{Request: req, Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv := l.call(0).argv
	i := argIndex(argv, "--session-id")
	if i < 0 || i+1 >= len(argv) || len(argv[i+1]) != 36 {
		t.Fatalf("a tracked step names its session up front: %s", joinArgs(argv))
	}
	if got := s.(ResumableSession).ResumeID(); got != argv[i+1] {
		t.Fatalf("ResumeID = %q before the turn ends, want %q", got, argv[i+1])
	}

	// An untracked dispatch keeps the old argv.
	if _, err := c.NewSession(context.Background(), Spec{Request: makeReq("pr_review", "p"), Cwd: "/wt"}, nil); err != nil {
		t.Fatal(err)
	}
	if argIndex(l.call(1).argv, "--session-id") >= 0 {
		t.Fatalf("untracked launch: %s", joinArgs(l.call(1).argv))
	}
}

func TestCLIClaudeResumeStepUsesResumeFlagInKeptCwd(t *testing.T) {
	l := &fakeLauncher{out: func([]string) (string, error) { return `{"session_id":"abc","result":"ok"}`, nil }}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	req := makeReq("pr_review", dispatch.ResumeNote)
	req.LiteralPrompt = true
	s, err := c.ResumeStep(context.Background(), Spec{Request: req, Cwd: "/kept"}, "abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, s)
	call := l.call(0)
	i := argIndex(call.argv, "--resume")
	if i < 0 || call.argv[i+1] != "abc" || call.dir != "/kept" || call.stdin != dispatch.ResumeNote {
		t.Fatalf("resume launch: dir=%q argv=%s stdin=%q", call.dir, joinArgs(call.argv), call.stdin)
	}
	if argIndex(call.argv, "--model") < 0 {
		t.Fatalf("the resumed turn keeps the step's model: %s", joinArgs(call.argv))
	}
	if err := s.(ResumeFailer).ResumeErr(); err != nil {
		t.Fatalf("a resumed turn that produced a session result did not fail: %v", err)
	}
}

func TestCLIResumeErrOnlyWhenSessionNotFound(t *testing.T) {
	l := &fakeLauncher{out: func([]string) (string, error) {
		return "No conversation found with session ID: abc", errors.New("exit status 1")
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	s, err := c.ResumeStep(context.Background(), Spec{Request: makeReq("k", "x"), Cwd: "/kept"}, "abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, s)
	if err := s.(ResumeFailer).ResumeErr(); err == nil || !strings.Contains(err.Error(), "No conversation found") {
		t.Fatalf("ResumeErr = %v", err)
	}
	// A fresh session's failure is a turn failure, never a resume failure.
	s2, _ := c.NewSession(context.Background(), Spec{Request: makeReq("k", "x"), Cwd: "/kept"}, nil)
	waitSession(t, s2)
	if err := s2.(ResumeFailer).ResumeErr(); err != nil {
		t.Fatalf("fresh ResumeErr = %v", err)
	}
}

func TestCLIOneshotRecipeCannotResumeStep(t *testing.T) {
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, nil)
	if c.CanResumeStep() {
		t.Fatal("codex exec is oneshot")
	}
	if _, err := c.ResumeStep(context.Background(), Spec{Request: makeReq("k", "x")}, "abc", nil); !errors.Is(err, ErrNoFollowup) {
		t.Fatalf("err = %v", err)
	}
}
