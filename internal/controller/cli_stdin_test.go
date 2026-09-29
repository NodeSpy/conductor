package controller

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// bigPrompt is past MAX_ARG_STRLEN (128 KiB): as one argv element, fork/exec
// refuses it with "argument list too long" — the failure a large PR's review
// prompt used to hit on the cli runtime.
var bigPrompt = strings.Repeat("diff line\n", 30000) // 300,000 bytes

// assertPromptOnStdin checks a launch carried the prompt on stdin and kept
// every argv element small enough for the kernel.
func assertPromptOnStdin(t *testing.T, call launchCall) {
	t.Helper()
	if !strings.Contains(call.stdin, bigPrompt) {
		t.Fatalf("the prompt must ride stdin (got %d stdin bytes)", len(call.stdin))
	}
	for _, a := range call.argv {
		if len(a) > maxCLIArgBytes {
			t.Fatalf("a %d-byte argv element would overflow MAX_ARG_STRLEN: %.80q…", len(a), a)
		}
	}
}

// The real subprocess path feeds stdin: a prompt far past the single-argument
// limit reaches the tool whole.
func TestStartCLIProcFeedsLargeStdin(t *testing.T) {
	if _, err := exec.LookPath("wc"); err != nil {
		t.Skip("wc unavailable")
	}
	proc, err := startCLIProc(context.Background(), "", nil, []string{"wc", "-c"}, bigPrompt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := proc.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := strings.TrimSpace(out); got != strconv.Itoa(len(bigPrompt)) {
		t.Fatalf("the tool read %s stdin bytes, want %d", got, len(bigPrompt))
	}
}

// Every built-in recipe turn — first launch, resume, lean decision — puts the
// prompt on stdin, so prompt size is bounded by the model, not the kernel.
func TestCLIBuiltinRecipesSendLargePromptsOnStdin(t *testing.T) {
	for _, tool := range []string{"claude-code", "codex"} {
		t.Run(tool, func(t *testing.T) {
			l := &fakeLauncher{out: func([]string) (string, error) { return `{"session_id":"s-1","result":"ok"}`, nil }}
			c := newCLIController("r", config.ControllerConfig{Transport: "cli", Tool: tool}, nil)
			c.launch = l.launch
			sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("review_requested", bigPrompt), Cwd: "/wt"}, nil)
			if err != nil {
				t.Fatalf("a large prompt must launch: %v", err)
			}
			waitSession(t, sess)
			assertPromptOnStdin(t, l.call(0))

			if tool != "claude-code" {
				return
			}
			ch, err := sess.Prompt(context.Background(), Message{Text: bigPrompt})
			if err != nil {
				t.Fatalf("a large follow-up must launch: %v", err)
			}
			for range ch {
			}
			assertPromptOnStdin(t, l.call(l.count()-1))
		})
	}
}

func TestCLILeanDecisionSendsLargeDocumentOnStdin(t *testing.T) {
	for _, tool := range []string{"claude-code", "codex"} {
		t.Run(tool, func(t *testing.T) {
			l := &fakeLauncher{}
			c := newCLIController("r", config.ControllerConfig{Transport: "cli", Tool: tool}, nil)
			c.launch = l.launch
			spec := decisionReq(t)
			spec.Request.Step.DecisionLaunch.Document = bigPrompt
			sess, err := c.NewSession(context.Background(), spec, nil)
			if err != nil {
				t.Fatalf("a large decision document must launch: %v", err)
			}
			waitSession(t, sess)
			assertPromptOnStdin(t, l.call(0))
		})
	}
}

// A remote launch is one ssh argument carrying the whole remote command; the
// prompt stays out of it and rides ssh's stdin instead.
func TestCLIRemoteLaunchKeepsPromptOutOfTheSSHCommand(t *testing.T) {
	prev := HostArgvPrefix
	HostArgvPrefix = func(string) ([]string, error) { return []string{"ssh", "buildbox", "--"}, nil }
	t.Cleanup(func() { HostArgvPrefix = prev })

	l := &fakeLauncher{}
	c := newCLIController("r", config.ControllerConfig{Transport: "cli", Tool: "claude-code", Host: "buildbox"}, nil)
	c.launch = l.launch
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("review_requested", bigPrompt), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatalf("a large remote prompt must launch: %v", err)
	}
	waitSession(t, sess)
	call := l.call(0)
	if call.argv[0] != "ssh" {
		t.Fatalf("expected an ssh launch, got %v", call.argv)
	}
	assertPromptOnStdin(t, call)
}

// An operator `command:` owns its argv, so its prompt stays an argument; one
// too big for the kernel fails up front with an actionable error rather than
// fork/exec's cryptic E2BIG — and nothing is launched.
func TestCLICustomCommandRejectsOversizedPromptArg(t *testing.T) {
	l := &fakeLauncher{}
	c := newCLIController("r", config.ControllerConfig{Transport: "cli", Command: []string{"mytool"}}, nil)
	c.launch = l.launch
	_, err := c.NewSession(context.Background(), Spec{Request: makeReq("review_requested", bigPrompt), Cwd: "/wt"}, nil)
	if err == nil || !strings.Contains(err.Error(), "MAX_ARG_STRLEN") {
		t.Fatalf("want the argument-size error, got %v", err)
	}
	if l.count() != 0 {
		t.Fatalf("an oversized launch must not start the tool (%d launches)", l.count())
	}
}
