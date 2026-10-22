package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// `bin:` replaces a built-in recipe's executable for EVERY launch it makes —
// the first turn and the --resume follow-up — so an operator's wrapper is what
// runs, never whatever the bare name resolves to on PATH.
func TestCLIClaudeBinDrivesLaunchAndResume(t *testing.T) {
	l := &fakeLauncher{out: func(argv []string) (string, error) {
		if argIndex(argv, "--resume") < 0 {
			return `{"session_id":"claude-abc","result":"done"}`, nil
		}
		return `{"result":"resumed"}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code", Bin: "/opt/wrap/claude"}, nil)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "start"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if got := l.call(0).argv[0]; got != "/opt/wrap/claude" {
		t.Fatalf("first launch runs %q, want the configured bin", got)
	}
	ch, err := sess.Prompt(context.Background(), Message{Text: "more"})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	resume := l.call(l.count() - 1)
	if argIndex(resume.argv, "--resume") < 0 || resume.argv[0] != "/opt/wrap/claude" {
		t.Fatalf("the --resume launch must run the configured bin too: %v", resume.argv)
	}
}

// A decide step's lean session is a launch like any other: it runs `bin:`.
func TestCLIBinDrivesTheDecideSession(t *testing.T) {
	for _, tc := range []struct{ tool, bin string }{
		{"claude-code", "/opt/wrap/claude"},
		{"codex", "/opt/wrap/codex"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			var argv []string
			c := newCLIController("x", config.ControllerConfig{Transport: "cli", Tool: tc.tool, Bin: tc.bin}, nil)
			c.launch = func(_ context.Context, _ string, _ []string, a []string, _ string) (cliProc, error) {
				argv = a
				if answer, ok := argAfter(a, "--output-last-message"); ok {
					_ = os.WriteFile(answer, []byte(`{"answers":{"refuted":0.5}}`), 0o600)
				}
				return &fakeProc{out: `{"type":"result","result":"","structured_output":{"answers":{"refuted":0.5}}}`}, nil
			}
			sess, err := c.NewSession(context.Background(), decisionReq(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			waitSession(t, sess)
			if len(argv) == 0 || argv[0] != tc.bin {
				t.Fatalf("decide session runs %v, want %s", argv, tc.bin)
			}
		})
	}
}

// Unset, every built-in recipe keeps running its own default name.
func TestCLIBinDefaultsToTheToolName(t *testing.T) {
	for tool, want := range map[string]string{"claude-code": "claude", "claude": "claude", "codex": "codex", "aider": "aider"} {
		l := &fakeLauncher{}
		c := newCLIController("x", config.ControllerConfig{Transport: "cli", Tool: tool}, nil)
		c.launch = l.launch
		sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "go"), Cwd: "/wt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		waitSession(t, sess)
		if got := l.call(0).argv[0]; got != want {
			t.Errorf("tool %s with no bin: runs %q, want %q", tool, got, want)
		}
	}
}

// A bare tool (no built-in recipe) runs `bin:` too.
func TestCLIBinDrivesABareTool(t *testing.T) {
	l := &fakeLauncher{}
	c := newCLIController("x", config.ControllerConfig{Transport: "cli", Tool: "aider", Bin: "/usr/local/bin/aider-wrap"}, nil)
	c.launch = l.launch
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "go"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if got := joinArgs(l.call(0).argv); got != "/usr/local/bin/aider-wrap go" {
		t.Fatalf("bare tool argv = %q", got)
	}
}

// `~` in bin: expands to this box's home for a local launch.
func TestCLIBinTildeExpandsLocally(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	l := &fakeLauncher{}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code", Bin: "~/.local/bin/claude-wrap"}, nil)
	c.launch = l.launch
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "go"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if got, want := l.call(0).argv[0], filepath.Join(home, ".local/bin/claude-wrap"); got != want {
		t.Fatalf("local launch runs %q, want %q", got, want)
	}
}

// A remote launch is left alone: this box's home means nothing on the host,
// and an operator `command:` is never rewritten.
func TestCLILocalBinLeavesRemoteAndCommandAlone(t *testing.T) {
	r := cliRecipeFor(config.ControllerConfig{Tool: "claude-code", Bin: "~/bin/claude"})
	cmd := r.cmd("", "p")
	if got := r.localBin("box", cmd).argv[0]; got != "~/bin/claude" {
		t.Fatalf("a remote launch must not expand ~ locally: %q", got)
	}
	if got := r.localBin("", cmd).argv[0]; got == "~/bin/claude" {
		t.Fatalf("a local launch must expand ~: %q", got)
	}
	custom := cliRecipeFor(config.ControllerConfig{Command: []string{"~/tool", "--x"}})
	if got := custom.localBin("", custom.cmd("", "p")).argv[0]; got != "~/tool" {
		t.Fatalf("an operator command: argv must pass through as written: %q", got)
	}
}
