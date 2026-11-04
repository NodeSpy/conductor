// Package gitsafe runs conductor's own git where a repository's content may
// be someone else's — an agent's workspace, a repository an agent's
// worktree shares — so that nothing in it steers git into running a
// command: hooks, fsmonitor, external diff and textconv drivers, the ext
// transport and submodule recursion are off, replace refs are ignored, and
// no inherited GIT_* variable redirects it. Filters a repository's own
// config names (clean/smudge) cannot be switched off by flag: a read of a
// workspace an agent wrote runs inside the agent sandbox instead
// (controller.ConfinedGit).
package gitsafe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Flags are the -c settings every hardened git carries. They propagate to
// the git processes it starts (GIT_CONFIG_PARAMETERS). An external diff is
// not switched off here (an empty diff.external makes porcelain diff exec
// ""): a diff caller passes --no-ext-diff --no-textconv.
var Flags = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "protocol.ext.allow=never",
	"-c", "submodule.recurse=false",
	"-c", "diff.ignoreSubmodules=all",
	"-c", "status.submoduleSummary=false",
	"-c", "core.untrackedCache=false",
}

// Args prefixes args with Flags.
func Args(args ...string) []string {
	return append(append([]string(nil), Flags...), args...)
}

// keepEnv are the GIT_* variables an operator sets on the daemon itself
// (trusted): where their own config lives, and how their transport
// authenticates.
var keepEnv = map[string]bool{
	"GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_NOSYSTEM": true,
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "GIT_SSH_VARIANT": true, "GIT_ASKPASS": true,
}

// Env is env with every other GIT_* variable dropped (GIT_DIR, GIT_WORK_TREE,
// GIT_CONFIG_PARAMETERS, GIT_EXTERNAL_DIFF … — a daemon started from a hook
// carries some), no prompt, replace refs off, and ssh connection sharing off
// (SSHCommand): the operator's GIT_SSH_COMMAND, else their own
// core.sshCommand, never a repository's. A GIT_SSH program is left as it is.
func Env(env []string) []string {
	out := make([]string, 0, len(env)+3)
	sshCmd, sshProg := "", false
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "GIT_SSH_COMMAND":
			sshCmd = v
		case k == "GIT_SSH":
			sshProg = true
			out = append(out, kv)
		case keepEnv[k]:
			out = append(out, kv)
		case strings.HasPrefix(k, "GIT_"):
		default:
			out = append(out, kv)
		}
	}
	if !sshProg {
		if sshCmd == "" {
			sshCmd = OperatorSSH()
		}
		out = append(out, "GIT_SSH_COMMAND="+SSHCommand(sshCmd))
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
}

// OperatorSSH is core.sshCommand from the operator's OWN config (global, then
// system) — never a repository's — or "ssh".
var OperatorSSH = sync.OnceValue(func() string {
	for _, scope := range []string{"--global", "--system"} {
		out, err := exec.Command("git", "config", scope, "--get", "core.sshCommand").Output()
		if err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	return "ssh"
})

// SSHCommand is an ssh command line with connection sharing off. An operator's
// ControlMaster multiplexes every ssh to a host over one connection, and a
// forge caps the sessions one connection carries: with a review's parallel
// fetches on it, a push queued for minutes or was refused ("Session open
// refused by peer"). Conductor's git gets its own connections instead. A
// command that is not ssh (plink, a wrapper script) is returned unchanged —
// it may not take ssh's -o.
func SSHCommand(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		cmd, f = "ssh", []string{"ssh"}
	}
	if b := filepath.Base(f[0]); b != "ssh" && b != "ssh.exe" {
		return cmd
	}
	return cmd + " -o ControlMaster=no -o ControlPath=none"
}

// Command is a hardened `git -C dir args…` ("" dir: the process's own).
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := Args(args...)
	if dir != "" {
		full = append([]string{"-C", dir}, full...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = Env(os.Environ())
	return cmd
}

// Output runs a hardened git and returns its stdout; an error carries its
// stderr.
func Output(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := Command(ctx, dir, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
