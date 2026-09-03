package controller

import (
	"context"
	"sync"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// launchCall records one cli launch.
type launchCall struct {
	dir  string
	env  []string
	argv []string
}

// fakeLauncher scripts cli process launches.
type fakeLauncher struct {
	mu    sync.Mutex
	calls []launchCall
	out   func(argv []string) (string, error)
}

func (l *fakeLauncher) launch(_ context.Context, dir string, env []string, argv []string) (cliProc, error) {
	l.mu.Lock()
	l.calls = append(l.calls, launchCall{dir: dir, env: env, argv: argv})
	l.mu.Unlock()
	out, err := "", error(nil)
	if l.out != nil {
		out, err = l.out(argv)
	}
	return &fakeProc{out: out, err: err}, nil
}

func (l *fakeLauncher) call(i int) launchCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[i]
}

func (l *fakeLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

type fakeProc struct {
	out string
	err error
}

func (p *fakeProc) Wait() (string, error) { return p.out, p.err }
func (p *fakeProc) Kill() error           { return nil }

func TestCLIOneshotRunInWorktree(t *testing.T) {
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, nil)
	c.launch = l.launch

	if c.Model() != ModelOneshot {
		t.Fatalf("codex recipe should be oneshot, got %q", c.Model())
	}

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix the bug"), Cwd: "/wt/o-r-7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	call := l.call(0)
	if call.dir != "/wt/o-r-7" {
		t.Fatalf("cli cwd = %q, want the worktree", call.dir)
	}
	if joinArgs(call.argv) != "codex exec fix the bug" {
		t.Fatalf("codex argv = %v", call.argv)
	}
	if !envHas(call.env, "GH_TOKEN", "utok") {
		t.Fatalf("cli env missing the user token: %v", call.env)
	}

	// A oneshot recipe refuses a follow-up.
	if _, err := sess.Prompt(context.Background(), Message{Text: "again"}); err != ErrNoFollowup {
		t.Fatalf("oneshot follow-up should be ErrNoFollowup, got %v", err)
	}
}

func TestCLIResumableClaudeCode(t *testing.T) {
	l := &fakeLauncher{out: func(argv []string) (string, error) {
		// First run emits a session id; a resume run echoes back.
		if argIndex(argv, "--resume") < 0 {
			return `{"session_id":"claude-abc","result":"done"}`, nil
		}
		return `{"result":"resumed"}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch

	if c.Model() != ModelResumable {
		t.Fatalf("claude-code recipe should be resumable, got %q", c.Model())
	}
	caps, _ := c.Initialize(context.Background())
	if !caps.SendFollowup {
		t.Fatal("a resumable recipe advertises SendFollowup")
	}

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "start"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	first := l.call(0)
	if argIndex(first.argv, "-p") < 0 || argIndex(first.argv, "start") < 0 {
		t.Fatalf("first claude argv = %v", first.argv)
	}

	// The follow-up resumes the captured tool session id.
	ch, err := sess.Prompt(context.Background(), Message{Text: "more"})
	if err != nil {
		t.Fatal(err)
	}
	var last Update
	for u := range ch {
		last = u
	}
	if last.Kind != UpdateDone {
		t.Fatalf("follow-up terminal update = %+v", last)
	}
	resume := l.call(l.count() - 1)
	ri := argIndex(resume.argv, "--resume")
	if ri < 0 || resume.argv[ri+1] != "claude-abc" {
		t.Fatalf("follow-up should --resume the captured session id, got %v", resume.argv)
	}
}

func TestCLICustomCommandRecipe(t *testing.T) {
	l := &fakeLauncher{}
	c := newCLIController("x", config.ControllerConfig{Transport: "cli", Command: []string{"mytool", "--flag"}}, nil)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "go"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if joinArgs(l.call(0).argv) != "mytool --flag go" {
		t.Fatalf("custom recipe argv = %v", l.call(0).argv)
	}
}
