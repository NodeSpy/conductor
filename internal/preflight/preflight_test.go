package preflight

import (
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// fakeLookPath resolves only the names in present; everything else "is not
// installed".
func fakeLookPath(present ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func hasLevel(findings []Finding, level, substr string) bool {
	for _, f := range findings {
		if f.Level == level && strings.Contains(f.What, substr) {
			return true
		}
	}
	return false
}

func TestCheckGit(t *testing.T) {
	fixerStep := config.Step{Type: "agent", Checkout: "checkout-pr"}
	judgeStep := config.Step{Type: "agent", Checkout: "checkout-pr", OutputSchema: map[string]any{"type": "object"}}
	pushStep := config.Step{Type: "agent", ExpectPush: true}
	cliWorktreeCfg := func() *config.Config {
		return &config.Config{
			Runtimes: config.RuntimeSet{"fixer-cli": {Use: "cli", Tool: "claude-code"}},
			Workflows: map[string]config.WorkflowDef{
				"fix": {Steps: []config.Step{{Type: "agent", Runtime: "fixer-cli", Workspace: config.Workspace{Isolation: "worktree"}}}},
			},
		}
	}

	cases := []struct {
		name       string
		cfg        *config.Config
		gitPresent bool
		wantLevel  string
	}{
		{
			name:       "git present: no finding regardless of steps",
			cfg:        &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{fixerStep}}}},
			gitPresent: true,
			wantLevel:  "",
		},
		{
			name:       "git missing, no push-capable step: warn",
			cfg:        &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{judgeStep}}}},
			gitPresent: false,
			wantLevel:  "warn",
		},
		{
			name:       "git missing, expect_push step: error",
			cfg:        &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{pushStep}}}},
			gitPresent: false,
			wantLevel:  "error",
		},
		{
			name:       "git missing, checkout-pr with no output_schema: error",
			cfg:        &config.Config{Triggers: config.TriggerList{{Name: "t", Steps: []config.Step{fixerStep}}}},
			gitPresent: false,
			wantLevel:  "error",
		},
		{
			name:       "git missing, checkout-pr WITH output_schema: warn only",
			cfg:        &config.Config{Triggers: config.TriggerList{{Name: "t", Steps: []config.Step{judgeStep}}}},
			gitPresent: false,
			wantLevel:  "warn",
		},
		{
			name:       "git missing, cli-transport worktree step: error",
			cfg:        cliWorktreeCfg(),
			gitPresent: false,
			wantLevel:  "error",
		},
		{
			name:       "git missing, no steps at all: warn",
			cfg:        &config.Config{},
			gitPresent: false,
			wantLevel:  "warn",
		},
		{
			name:       "git missing, push step nested in a parallel branch: error",
			cfg:        &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{{Parallel: &config.ParallelSpec{Branches: [][]config.Step{{pushStep}}}}}}}},
			gitPresent: false,
			wantLevel:  "error",
		},
		{
			name:       "git missing, push step is a named check: error",
			cfg:        &config.Config{Checks: map[string]config.Step{"c": pushStep}},
			gitPresent: false,
			wantLevel:  "error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{LookPath: fakeLookPath()}
			if tc.gitPresent {
				env.LookPath = fakeLookPath("git")
			}
			findings := checkGit(tc.cfg, env)
			switch tc.wantLevel {
			case "":
				if len(findings) != 0 {
					t.Fatalf("want no findings, got %+v", findings)
				}
			default:
				if len(findings) != 1 || findings[0].Level != tc.wantLevel {
					t.Fatalf("want one %s finding, got %+v", tc.wantLevel, findings)
				}
			}
		})
	}
}

func TestCheckCLIRuntimeTools(t *testing.T) {
	cfg := &config.Config{
		Runtimes: config.RuntimeSet{
			"claude": {Use: "cli", Tool: "claude-code"},
			"codex":  {Use: "cli", Tool: "codex"},
			"remote": {Use: "cli", Tool: "claude-code", Host: "box1"}, // skipped: binary lives on box1
			"native": {Use: "paseo"},                                  // not cli: skipped regardless
		},
	}

	t.Run("both tools present: no findings", func(t *testing.T) {
		findings := checkCLIRuntimeTools(cfg, Env{LookPath: fakeLookPath("claude", "codex")})
		if len(findings) != 0 {
			t.Fatalf("want no findings, got %+v", findings)
		}
	})

	t.Run("claude missing: one error naming the runtime", func(t *testing.T) {
		findings := checkCLIRuntimeTools(cfg, Env{LookPath: fakeLookPath("codex")})
		if len(findings) != 1 {
			t.Fatalf("want exactly one finding (remote/native skipped), got %+v", findings)
		}
		if findings[0].Level != "error" || !strings.Contains(findings[0].What, `"claude"`) || !strings.Contains(findings[0].What, "claude") {
			t.Fatalf("finding = %+v, want an error naming claude", findings[0])
		}
	})

	t.Run("explicit command recipe checks argv[0]", func(t *testing.T) {
		cfg := &config.Config{Runtimes: config.RuntimeSet{"custom": {Use: "cli", Command: []string{"my-agent-tool", "run"}}}}
		findings := checkCLIRuntimeTools(cfg, Env{LookPath: fakeLookPath()})
		if len(findings) != 1 || !strings.Contains(findings[0].What, "my-agent-tool") {
			t.Fatalf("findings = %+v, want one naming my-agent-tool", findings)
		}
	})
}

func TestCheckCommandSteps(t *testing.T) {
	cfg := &config.Config{
		Workflows: map[string]config.WorkflowDef{
			"w": {Steps: []config.Step{
				{Type: "command", Command: config.Argv{"missing-tool", "--flag"}},
				{Type: "command", Command: config.Argv{"present-tool"}},
				{Type: "command", Command: config.Argv{"missing-tool"}},              // dedup: only one finding
				{Type: "command", Command: config.Argv{"remote-tool"}, Host: "box1"}, // skipped: remote
			}},
		},
	}
	findings := checkCommandSteps(cfg, Env{LookPath: fakeLookPath("present-tool")})
	if len(findings) != 1 {
		t.Fatalf("want exactly one deduplicated warning, got %+v", findings)
	}
	if findings[0].Level != "warn" || !strings.Contains(findings[0].What, "missing-tool") {
		t.Fatalf("finding = %+v, want a warn naming missing-tool", findings[0])
	}
}

func TestRegisterExtendsCheck(t *testing.T) {
	defer func(prev []CheckFunc) { registered = prev }(registered)
	registered = nil

	called := false
	Register(func(cfg *config.Config, env Env) []Finding {
		called = true
		return []Finding{{Level: "info", What: "custom check ran", Why: "test"}}
	})
	findings := Check(&config.Config{}, Env{LookPath: fakeLookPath("git")})
	if !called {
		t.Fatal("registered check was not invoked")
	}
	if !hasLevel(findings, "info", "custom check ran") {
		t.Fatalf("Check() did not include the registered finding: %+v", findings)
	}
}

func TestCheckAggregatesEverything(t *testing.T) {
	cfg := &config.Config{
		Runtimes: config.RuntimeSet{"claude": {Use: "cli", Tool: "claude-code"}},
		Workflows: map[string]config.WorkflowDef{
			"w": {Steps: []config.Step{
				{Type: "agent", ExpectPush: true},
				{Type: "command", Command: config.Argv{"missing-tool"}},
			}},
		},
	}
	findings := Check(cfg, Env{LookPath: fakeLookPath()})
	if !hasLevel(findings, "error", "git is not installed") {
		t.Errorf("missing git error: %+v", findings)
	}
	if !hasLevel(findings, "error", `"claude"`) {
		t.Errorf("missing cli-tool error: %+v", findings)
	}
	if !hasLevel(findings, "warn", "missing-tool") {
		t.Errorf("missing command warning: %+v", findings)
	}
}
