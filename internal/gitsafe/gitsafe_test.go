package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// TestArgsHardensEveryCall asserts the fixed -c overrides are always present,
// and --no-ext-diff is appended only for a diff-shaped subcommand.
func TestArgsHardensEveryCall(t *testing.T) {
	requireGit(t)
	for _, want := range []string{
		"core.hooksPath=/dev/null",
		"core.fsmonitor=false",
		"protocol.ext.allow=never",
		"diff.external=",
	} {
		found := false
		for _, a := range Args("status") {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("Args(status) missing -c %s: %v", want, Args("status"))
		}
	}
	if !containsArg(Args("diff", "HEAD"), "--no-ext-diff") {
		t.Fatalf("Args(diff, HEAD) missing --no-ext-diff: %v", Args("diff", "HEAD"))
	}
	if containsArg(Args("worktree", "add"), "--no-ext-diff") {
		t.Fatalf("Args(worktree, add) should not carry --no-ext-diff: %v", Args("worktree", "add"))
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestTrustedSSHCommandIgnoresLocalConfig is the core safety property of this
// package: the ssh command git shells out to is resolved from the OPERATOR's
// own (global/system) config, never from a repo's local .git/config — a
// tampered clone or PR branch must not be able to choose what runs.
//
// It points HOME and GIT_CONFIG_GLOBAL at a fixture global config carrying a
// marker sshCommand, then proves a DIFFERENT local-config value (as a repo's
// .git/config would carry) has no effect on what Args() resolves.
func TestTrustedSSHCommandIgnoresLocalConfig(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	globalConfig := filepath.Join(dir, "gitconfig-global")
	if err := os.WriteFile(globalConfig, []byte("[core]\n\tsshCommand = ssh-marker-global\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(dir, "does-not-exist"))
	ResetForTest()
	t.Cleanup(ResetForTest)

	// A repo whose LOCAL config sets a different sshCommand — this must never
	// win, and Args does not even take a repo path, by construction.
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q")
	run(t, repo, "git", "config", "core.sshCommand", "ssh-marker-local")

	args := Args("fetch")
	if !containsArg(args, "core.sshCommand=ssh-marker-global") {
		t.Fatalf("Args() did not resolve the trusted global sshCommand; got %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, "ssh-marker-local") {
			t.Fatalf("Args() leaked the repo-local sshCommand: %v", args)
		}
	}
}

// TestTrustedSSHCommandDefaultsToSSH covers the case where neither global nor
// system config sets core.sshCommand: the resolution must fall back to the
// plain "ssh" default rather than erroring or leaving the flag empty (an
// empty -c core.sshCommand= would itself be a footgun — git treats that as
// "no ssh command", which is not the same as "the default").
func TestTrustedSSHCommandDefaultsToSSH(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "does-not-exist-global"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(dir, "does-not-exist-system"))
	ResetForTest()
	t.Cleanup(ResetForTest)

	args := Args("fetch")
	if !containsArg(args, "core.sshCommand=ssh") {
		t.Fatalf("Args() did not default to plain ssh; got %v", args)
	}
}

// TestCommandRunsInDir proves Command actually scopes to dir via -C and
// disables terminal prompting.
func TestCommandRunsInDir(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.name", "T")
	run(t, dir, "git", "config", "user.email", "t@example.test")

	cmd := Command(context.Background(), dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Fatalf("git ran in %q, want %q", got, want)
	}
	foundPrompt := false
	for _, e := range cmd.Env {
		if e == "GIT_TERMINAL_PROMPT=0" {
			foundPrompt = true
		}
	}
	if !foundPrompt {
		t.Fatal("Command did not set GIT_TERMINAL_PROMPT=0")
	}
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", strings.Join(argv, " "), err, b)
	}
}
