// Package fixer is the shared "do the agent's work" helper for the e2e harness's
// fake controller runtimes (fakecli → claude/codex, fakeacp, the fake `opencode`
// serve, and the fake `agent-deck`). Conductor drives each of those through its
// real controller code, which provisions a PR worktree and hands the runtime the
// acts-as-the-user identity via env; this package makes the driven "agent" perform
// a deterministic fixer edit, commit it as the user, and push it to the local
// forge — so `forge_has_conductor_commit` passes for every controller, exactly as
// it does for the built-in paseo path (test/e2e/services/fakepaseo).
//
// It does REAL git in the conductor-provisioned worktree (the controller sets the
// runtime process's cwd to that worktree, and passes the same path in-protocol),
// with NO LLM and NO secrets. NOT part of the shipped product; harness-only.
package fixer

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Apply makes the scripted fixer edit in dir, commits it as the acts-as-the-user
// identity (GIT_AUTHOR_*/GIT_COMMITTER_* that conductor passes via dispatch.AgentEnv),
// and pushes the current branch back to origin (the local forge). runtime is a
// short label for the controller runtime doing the work (e.g. "cli:claude-code"),
// recorded in the commit body and a marker file so per-controller attribution is
// visible on the forge. A non-git or empty dir is a no-op (e.g. a resumed session
// with no dedicated worktree) — it returns nil so the turn still "succeeds".
func Apply(dir, runtime, prompt string) error {
	if dir == "" || !isGitRepo(dir) {
		return nil
	}
	name := envOr("GIT_AUTHOR_NAME", "fakeagent")
	email := envOr("GIT_AUTHOR_EMAIL", "fakeagent@example.test")

	// A deterministic, controller-attributed edit so the commit is observable and
	// each controller's row on the forge is distinguishable.
	marker := filepath.Join(dir, "CONDUCTOR_FIX.md")
	line := fmt.Sprintf("conductor fix by %s: %s\n", runtime, firstLine(prompt))
	if f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString(line)
		_ = f.Close()
	}

	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email,
	)
	git(dir, env, "add", "-A")
	// The `conductor:` subject prefix is what the harness asserts on
	// (forge_has_conductor_commit); the runtime label lives in the body.
	msg := fmt.Sprintf("conductor: %s\n\nvia %s", firstLine(prompt), runtime)
	// -c user.* covers a git build that honors only committer config.
	git(dir, env, "-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "-m", msg, "--allow-empty")

	branch := gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" || branch == "HEAD" {
		branch = "conductor-work"
	}
	pushErr := gitErr(dir, env, "push", "origin", "HEAD:refs/heads/"+branch)
	if strings.Contains(prompt, SandboxProbeMarker) {
		sandboxProbe(dir, env, name, email, pushErr)
	}
	return nil
}

// SandboxProbeMarker in a prompt makes the fake agent record what it can and
// cannot do from where it runs (group Z: conductor's agent sandbox) into
// SANDBOX_PROBE.md, committed — so the record reaches the forge only by the
// way the sandbox allows: conductor's own push.
const SandboxProbeMarker = "[[sandbox-probe]]"

func sandboxProbe(dir string, env []string, name, email string, ownPush error) {
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s: %s\n", k, v) }
	if _, err := os.ReadFile("/home/conductor/.ssh/id_fake"); err != nil {
		line("secret", "denied")
	} else {
		line("secret", "READABLE")
	}
	if _, err := os.ReadFile("/home/conductor/data/state.json"); err != nil {
		line("conductor-state", "denied")
	} else {
		line("conductor-state", "READABLE")
	}
	line("home", os.Getenv("HOME"))
	if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" && os.Getenv("SSH_AUTH_SOCK") == "" {
		line("credentials-env", "none")
	} else {
		line("credentials-env", "PRESENT")
	}
	if ownPush != nil {
		line("own-push", "refused")
	} else {
		line("own-push", "SUCCEEDED")
	}
	if c, err := net.DialTimeout("tcp", "forge:9418", 3*time.Second); err != nil {
		line("network", "unreachable")
	} else {
		c.Close()
		line("network", "REACHABLE")
	}
	out, err := exec.Command("conductor", "call", "git.push").CombinedOutput()
	line("git.push", verdict(out, err))
	out, err = exec.Command("conductor", "call", "git.push", "--branch", "main").CombinedOutput()
	line("git.push-other-ref", verdict(out, err))
	out, err = exec.Command("conductor", "call", "step.done", "--reason", "sandbox probe").CombinedOutput()
	line("step.done", verdict(out, err))
	_ = os.WriteFile(filepath.Join(dir, "SANDBOX_PROBE.md"), []byte(b.String()), 0o644)
	git(dir, env, "add", "SANDBOX_PROBE.md")
	git(dir, env, "-c", "user.name="+name, "-c", "user.email="+email, "commit", "-m", "conductor: sandbox probe record")
}

// verdict is "ok" or "refused: <first line of the error>".
func verdict(out []byte, err error) string {
	if err == nil {
		return "ok"
	}
	return "refused: " + firstLine(strings.TrimSpace(string(out)))
}

// gitErr is git that reports its failure.
func gitErr(dir string, env []string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env
	out, err := c.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixer: git %s: %v: %s\n", strings.Join(args, " "), err, out)
	}
	return err
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func git(dir string, env []string, args ...string) {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env
	if out, err := c.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "fixer: git %s: %v: %s\n", strings.Join(args, " "), err, out)
	}
}

func gitOut(dir string, args ...string) string {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, _ := c.Output()
	return strings.TrimSpace(string(out))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return strings.TrimSpace(s)
}
