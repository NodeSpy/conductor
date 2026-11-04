package gitsafe

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const noMux = " -o ControlMaster=no -o ControlPath=none"

func TestSSHCommand(t *testing.T) {
	for in, want := range map[string]string{
		"":                     "ssh" + noMux,
		"ssh":                  "ssh" + noMux,
		"ssh -i ~/.ssh/deploy": "ssh -i ~/.ssh/deploy" + noMux,
		"/usr/bin/ssh -4":      "/usr/bin/ssh -4" + noMux,
		// Not ssh: it may not take -o, so it is left alone.
		"plink -batch":        "plink -batch",
		"/opt/bin/git-ssh.sh": "/opt/bin/git-ssh.sh",
	} {
		if got := SSHCommand(in); got != want {
			t.Errorf("SSHCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvTurnsSSHConnectionSharingOff(t *testing.T) {
	get := func(env []string, k string) []string {
		var vs []string
		for _, kv := range env {
			if key, v, _ := strings.Cut(kv, "="); key == k {
				vs = append(vs, v)
			}
		}
		return vs
	}
	// The operator's own GIT_SSH_COMMAND is kept, with sharing off, once.
	env := Env([]string{"PATH=/bin", "GIT_SSH_COMMAND=ssh -i k", "GIT_DIR=/x"})
	if got := get(env, "GIT_SSH_COMMAND"); !slices.Equal(got, []string{"ssh -i k" + noMux}) {
		t.Fatalf("GIT_SSH_COMMAND = %q", got)
	}
	if get(env, "GIT_DIR") != nil {
		t.Fatal("GIT_DIR kept")
	}
	// None set: the operator's core.sshCommand (or ssh), never a repository's.
	if got := get(Env([]string{"PATH=/bin"}), "GIT_SSH_COMMAND"); !slices.Equal(got, []string{SSHCommand(OperatorSSH())}) {
		t.Fatalf("GIT_SSH_COMMAND = %q, want the operator's %q", got, SSHCommand(OperatorSSH()))
	}
	// A GIT_SSH program takes no options: it is left to run as configured.
	env = Env([]string{"GIT_SSH=/usr/bin/plink"})
	if get(env, "GIT_SSH_COMMAND") != nil || !slices.Equal(get(env, "GIT_SSH"), []string{"/usr/bin/plink"}) {
		t.Fatalf("GIT_SSH env = %q", env)
	}
}

// Through a real git: the ssh it runs for a fetch is handed the options. The
// fake ssh records its arguments and fails, as an unreachable host would.
func TestGitRunsSSHWithoutConnectionSharing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake ssh")
	}
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argv+"\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", fake)
	_, _ = Output(context.Background(), "", "ls-remote", "git@forge.invalid:org/repo.git")
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("git never ran the ssh command: %v", err)
	}
	args := " " + strings.Join(strings.Fields(string(b)), " ") + " "
	if !strings.Contains(args, noMux+" ") {
		t.Fatalf("ssh ran with %q, want connection sharing off (%q)", args, noMux)
	}
}
