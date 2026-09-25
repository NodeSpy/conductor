package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func decisionReq(t *testing.T) Spec {
	t.Helper()
	req := makeReq("merge_conflict", "SYSTEM\n\nQUESTIONS…<document>…</document>")
	req.Step.DecisionLaunch = &config.DecisionLaunch{
		System:   "Evaluate every question using only the supplied document.",
		Document: "QUESTIONS…<document>{{.gh_token}}</document>",
		Schema:   map[string]any{"type": "object", "required": []any{"answers"}},
	}
	req.Action.OutputSchema = req.Step.DecisionLaunch.Schema
	return Spec{Request: req, Cwd: "/wt/should-not-be-used"}
}

func argAfter(argv []string, flag string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1], true
		}
	}
	return "", false
}

// A decide session on claude-code runs LEAN: no tools, no MCP, no slash
// commands, the adapter's system prompt, native --json-schema — and never
// --dangerously-skip-permissions or --bare (which would drop the OAuth login).
func TestCLIClaudeDecisionRunsLean(t *testing.T) {
	var dir string
	l := &fakeLauncher{out: func(argv []string) (string, error) {
		return `{"type":"result","result":"","structured_output":{"answers":{"refuted":0.9}},"is_error":false}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = func(ctx context.Context, d string, env []string, argv []string) (cliProc, error) {
		dir = d
		if _, err := os.Stat(d); err != nil {
			t.Errorf("the scratch dir must exist while the tool runs: %v", err)
		}
		return l.launch(ctx, d, env, argv)
	}
	sess, err := c.NewSession(context.Background(), decisionReq(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	argv := l.call(0).argv
	if tools, ok := argAfter(argv, "--tools"); !ok || tools != "" {
		t.Fatalf("--tools must be passed empty (no tools at all): %v", argv)
	}
	for _, want := range []string{"--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"} {
		if argIndex(argv, want) < 0 {
			t.Errorf("lean session missing %s: %v", want, argv)
		}
	}
	for _, banned := range []string{"--dangerously-skip-permissions", "--bare"} {
		if argIndex(argv, banned) >= 0 {
			t.Errorf("lean session must not pass %s: %v", banned, argv)
		}
	}
	if sp, _ := argAfter(argv, "--system-prompt"); !strings.HasPrefix(sp, "Evaluate every question") {
		t.Errorf("the adapter's system prompt replaces Claude Code's: %q", sp)
	}
	if sch, _ := argAfter(argv, "--json-schema"); !json.Valid([]byte(sch)) {
		t.Errorf("--json-schema must carry the schema as JSON: %q", sch)
	}
	if p, _ := argAfter(argv, "-p"); p != "QUESTIONS…<document>{{.gh_token}}</document>" {
		t.Errorf("the user turn is the Document, verbatim: %q", p)
	}
	if m, _ := argAfter(argv, "--model"); m != "anthropic/claude" {
		t.Errorf("the resolved model rides the argv: %v", argv)
	}
	if dir == "/wt/should-not-be-used" || !strings.Contains(dir, "conductor-decide-") {
		t.Fatalf("a decision runs in its own scratch dir, got %q", dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the scratch dir must be removed when the run ends: %v", err)
	}
	if out := sess.(OutputCapturer).Output(); out != `{"answers":{"refuted":0.9}}` {
		t.Fatalf("the answer is the envelope's structured_output: %q", out)
	}
}

// codex: read-only sandbox, ephemeral, the schema file, and the answer read
// from --output-last-message (its stderr logs share the captured stream).
func TestCLICodexDecisionReadsTheAnswerFile(t *testing.T) {
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, nil)
	var argv []string
	var dir string
	c.launch = func(_ context.Context, d string, _ []string, a []string) (cliProc, error) {
		argv, dir = a, d
		schema, _ := argAfter(a, "--output-schema")
		if raw, err := os.ReadFile(schema); err != nil || !json.Valid(raw) {
			t.Errorf("the schema file must exist and hold the schema: %v", err)
		}
		answer, _ := argAfter(a, "--output-last-message")
		if err := os.WriteFile(answer, []byte(`{"answers":{"refuted":0.99}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return &fakeProc{out: "codex log line {not json}\n{\"answers\":{\"refuted\":0.1}}\ntokens used 13,536"}, nil
	}
	sess, err := c.NewSession(context.Background(), decisionReq(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if sb, _ := argAfter(argv, "--sandbox"); sb != "read-only" {
		t.Errorf("codex decision sandbox = %q, want read-only", sb)
	}
	for _, want := range []string{"--ephemeral", "--skip-git-repo-check"} {
		if argIndex(argv, want) < 0 {
			t.Errorf("missing %s: %v", want, argv)
		}
	}
	if cd, _ := argAfter(argv, "--cd"); cd != dir {
		t.Errorf("--cd must be the scratch dir: %q vs %q", cd, dir)
	}
	if last := argv[len(argv)-1]; !strings.HasPrefix(last, "Evaluate every question") || !strings.Contains(last, "<document>") {
		t.Errorf("codex has no system-prompt flag, so the system prompt leads the prompt: %q", last)
	}
	if out := sess.(OutputCapturer).Output(); out != `{"answers":{"refuted":0.99}}` {
		t.Fatalf("the answer comes from the file, not the log-mixed stream: %q", out)
	}
	if _, err := os.Stat(filepath.Dir(dir + "/x")); !os.IsNotExist(err) {
		t.Fatalf("the scratch dir must be removed: %v", err)
	}
}

// Anything that is not a decide step keeps today's full-agent recipe.
func TestCLIAgentStepKeepsTheFullRecipe(t *testing.T) {
	l := &fakeLauncher{out: func([]string) (string, error) { return `{"result":"done"}`, nil }}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix it"), Cwd: "/wt/x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if argIndex(l.call(0).argv, "--dangerously-skip-permissions") < 0 || argIndex(l.call(0).argv, "--tools") >= 0 {
		t.Fatalf("an agent step's recipe must be unchanged: %v", l.call(0).argv)
	}
	if l.call(0).dir != "/wt/x" {
		t.Fatalf("an agent step runs in its worktree: %q", l.call(0).dir)
	}
}

// A decision pinned to a remote host can't use a local scratch dir: it runs
// the ordinary recipe there rather than failing.
func TestCLIRemoteDecisionFallsBackToTheFullRecipe(t *testing.T) {
	prev := HostArgvPrefix
	HostArgvPrefix = func(string) ([]string, error) { return []string{"ssh", "buildbox", "--"}, nil }
	t.Cleanup(func() { HostArgvPrefix = prev })
	l := &fakeLauncher{}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code", Host: "buildbox"}, nil)
	c.launch = l.launch
	sess, err := c.NewSession(context.Background(), decisionReq(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if l.count() == 0 {
		t.Fatal("expected a launch")
	}
	for _, a := range l.call(0).argv {
		if a == "--json-schema" {
			t.Fatalf("a remote decision must not use the local-scratch lean recipe: %v", l.call(0).argv)
		}
	}
}
