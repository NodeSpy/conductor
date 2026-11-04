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
	"strings"
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
// carries some), no prompt, and replace refs off.
func Env(env []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case keepEnv[k]:
			out = append(out, kv)
		case strings.HasPrefix(k, "GIT_"):
		default:
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
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
