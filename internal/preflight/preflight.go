// Package preflight audits a loaded config against the actual box conductor
// is about to run on — the things `conductor validate` and a schema check
// cannot see, because they only look at the YAML: is the `git` binary
// present, is a configured cli runtime's tool actually installed, is a
// `command:` step's binary on PATH. None of these are config MISTAKES (the
// config may be perfectly valid on a box that has everything), so they are
// reported as Findings rather than load errors — `conductor validate` turns
// an error Finding into a non-zero exit; the daemon logs every Finding at
// boot but never refuses to start over one (a transient PATH problem must
// not crash-loop an otherwise-fine deployment).
//
// Register lets another package (e.g. the agent jail) add its own checks
// without conductor validate/run needing to know about it — one list of
// findings, however many packages contribute to it.
package preflight

import (
	"fmt"
	"os/exec"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// Finding is one preflight observation. Level is "error" (validate fails,
// the daemon logs it loudly but still starts), "warn", or "info".
type Finding struct {
	Level string
	What  string
	Why   string
}

// Env is the actual-box context a check runs against, injected so tests can
// fake it without touching the real PATH/OS/uid.
type Env struct {
	// LookPath resolves a binary name the way exec.LookPath does. Nil means
	// exec.LookPath itself.
	LookPath func(string) (string, error)
	GOOS     string
	Euid     int
}

// lookPath applies Env.LookPath, defaulting to the real exec.LookPath.
func (e Env) lookPath(bin string) error {
	lp := e.LookPath
	if lp == nil {
		lp = exec.LookPath
	}
	_, err := lp(bin)
	return err
}

// CheckFunc is one contributor to the overall Finding list — see Register.
type CheckFunc func(cfg *config.Config, env Env) []Finding

// registered holds every check function beyond the built-ins below, added
// via Register.
var registered []CheckFunc

// Register adds fn to the checks Check runs, so another package can extend
// preflight without conductor's validate/boot paths importing it directly.
// Not safe to call concurrently with Check; call it from an init() or before
// the daemon starts serving, as every other registration mechanism in this
// codebase does.
func Register(fn CheckFunc) {
	registered = append(registered, fn)
}

// Check runs every built-in check plus every one added via Register, against
// cfg and the given Env, and returns every Finding in a stable order (built-ins
// first, then registered checks in registration order).
func Check(cfg *config.Config, env Env) []Finding {
	var out []Finding
	out = append(out, checkGit(cfg, env)...)
	out = append(out, checkCLIRuntimeTools(cfg, env)...)
	out = append(out, checkCommandSteps(cfg, env)...)
	for _, fn := range registered {
		out = append(out, fn(cfg, env)...)
	}
	return out
}

// ---- (a) git ---------------------------------------------------------

// checkGit reports whether `git` is missing, and how severe that is: an
// error when some configured step actually needs it to LAND a change (a
// fixer), a warning otherwise (reviews still work — see internal/gitwt's
// go-git fallback and internal/gitdiff's).
func checkGit(cfg *config.Config, env Env) []Finding {
	if err := env.lookPath("git"); err == nil {
		return nil
	}
	if anyStepCanPush(cfg) {
		return []Finding{{
			Level: "error",
			What:  "git is not installed",
			Why:   "fixers need git (commit, rebase, conflict resolution); install git",
		}}
	}
	return []Finding{{
		Level: "warn",
		What:  "git is not installed",
		Why:   "reviews fall back to go-git full clones and .conductor/pr.diff",
	}}
}

// anyStepCanPush reports whether any configured agent step is one whose job
// is to land a change: a cli-runtime step with a worktree workspace, a step
// explicitly marked expect_push, or a checkout-pr agent step with no
// output_schema (a judge/decider reports a structured verdict instead of
// pushing — an output_schema is what tells the two apart statically).
func anyStepCanPush(cfg *config.Config) bool {
	found := false
	walkAllSteps(cfg, func(s config.Step) {
		if found || s.Form() != "agent" {
			return
		}
		switch {
		case s.ExpectPush:
			found = true
		case s.Checkout == "checkout-pr" && len(s.OutputSchema) == 0:
			found = true
		case isCLIWorktreeStep(cfg, s):
			found = true
		}
	})
	return found
}

// isCLIWorktreeStep reports whether s dispatches on a `cli` transport runtime
// (the recipe-driven fallback controller — internal/controller/cli.go) with a
// worktree workspace: exactly the shape gitwt.ProvisionWorktree serves, and
// the one that needs real git to push a fix.
func isCLIWorktreeStep(cfg *config.Config, s config.Step) bool {
	if s.Workspace.Isolation != "worktree" {
		return false
	}
	name := s.Runtime
	if name == "" {
		name = cfg.DefaultRuntimeName()
	}
	if name == "" {
		return false
	}
	cc, ok := cfg.MergedControllers()[name]
	return ok && isCLIController(cc)
}

// isCLIController reports whether cc dispatches through the bare-runner
// fallback controller (internal/controller/cli.go): either spelling the
// registry itself accepts — the connectors-model `use: cli` runtime (which
// carries Type "cli", no transport: see RuntimeConfig.Controller) or the
// legacy `transport: cli` controller entry.
func isCLIController(cc config.ControllerConfig) bool {
	return cc.Type == "cli" || cc.EffectiveTransport() == "cli"
}

// ---- (b) cli runtime tool binaries -------------------------------------

// checkCLIRuntimeTools errors on every configured `cli`-transport runtime
// whose tool binary is missing — every dispatch on it would fail at launch,
// so this is always an error, never a warning. A runtime routed through a
// `host:` is skipped: its binary needs to exist on THAT box, not this one.
func checkCLIRuntimeTools(cfg *config.Config, env Env) []Finding {
	merged := cfg.MergedControllers()
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Finding
	for _, name := range names {
		cc := merged[name]
		if !isCLIController(cc) || cc.Host != "" {
			continue
		}
		bin := cliToolBinary(cc)
		if bin == "" {
			continue
		}
		if err := env.lookPath(bin); err != nil {
			out = append(out, Finding{
				Level: "error",
				What:  fmt.Sprintf("runtime %q needs %q, which is not installed", name, bin),
				Why:   fmt.Sprintf("every dispatch on runtime %q would fail at launch", name),
			})
		}
	}
	return out
}

// cliToolBinary mirrors cliRecipeFor's tool selection
// (internal/controller/cli.go): an explicit command's own argv[0] wins;
// otherwise the tool/agent name picks a known binary (claude-code and its
// "claude" alias both run the `claude` binary; codex runs `codex`); any other
// name is assumed to BE the binary name.
func cliToolBinary(cc config.ControllerConfig) string {
	tool := cc.Tool
	if tool == "" {
		tool = cc.Agent
	}
	if len(cc.Command) > 0 {
		if tool != "" {
			return tool
		}
		return cc.Command[0]
	}
	switch tool {
	case "claude-code", "claude":
		return "claude"
	case "codex":
		return "codex"
	default:
		return tool
	}
}

// ---- (c) command: steps -------------------------------------------------

// checkCommandSteps warns (never errors — an operator's own command: recipe
// is their call) on every distinct command[0] named by a step's `command:`
// that is not on PATH. A step routed through `host:`/`ssh:` is skipped: the
// binary needs to exist on that box, not this one.
func checkCommandSteps(cfg *config.Config, env Env) []Finding {
	var out []Finding
	seen := map[string]bool{}
	walkAllSteps(cfg, func(s config.Step) {
		if len(s.Command) == 0 || s.Host != "" || s.SSH != nil {
			return
		}
		bin := s.Command[0]
		if bin == "" || seen[bin] {
			return
		}
		if err := env.lookPath(bin); err != nil {
			seen[bin] = true
			out = append(out, Finding{
				Level: "warn",
				What:  fmt.Sprintf("command %q is not installed", bin),
				Why:   "a command step naming it will fail at run time",
			})
		}
	})
	return out
}

// ---- step walking ---------------------------------------------------------

// walkAllSteps visits every step in the config: every workflow's steps,
// every trigger's inline steps, and every named check (checks: entries are
// themselves ordinary steps — #36 §16).
func walkAllSteps(cfg *config.Config, fn func(config.Step)) {
	for _, wf := range cfg.Workflows {
		walkSteps(wf.Steps, fn)
	}
	for _, tr := range cfg.Triggers {
		walkSteps(tr.Steps, fn)
	}
	for _, ck := range cfg.Checks {
		walkSteps([]config.Step{ck}, fn)
	}
}

// walkSteps visits steps and recurses into the two places a step nests more
// steps: a parallel step's branches, and a compensate action's own step.
// A workflow-call step's own steps live at cfg.Workflows[name] and are
// visited there directly — recursing through Call/Workflow here would double
// -visit them for every step that calls it.
func walkSteps(steps []config.Step, fn func(config.Step)) {
	for _, s := range steps {
		fn(s)
		if s.Compensate != nil {
			walkSteps([]config.Step{*s.Compensate}, fn)
		}
		if s.Parallel != nil {
			for _, branch := range s.Parallel.Branches {
				walkSteps(branch, fn)
			}
		}
	}
}
