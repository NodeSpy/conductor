package dispatch

import (
	"context"
	"os/exec"

	"github.com/NodeSpy/paseo-conductor/internal/hosts"
)

// paseoCommand builds the exec.Cmd for one paseo CLI invocation: the local
// binary, or — for a paseo runtime with `host:` — the same argv wrapped in an
// ssh launch on the remote box. The remote command exports the host's `env:`
// and cd's to its `cwd:` inside the ssh channel, so nothing secret rides this
// box's argv. Every paseo subcommand (run/ls/inspect/send/archive/clone/
// workspace/wait) goes through here, which is what makes a remote paseo
// runtime behave like a local one: the CLI is the contract, and it all
// executes on the box that owns the daemon.
func paseoCommand(ctx context.Context, bin string, remote *hosts.Target, args ...string) *exec.Cmd {
	if remote == nil {
		return exec.CommandContext(ctx, bin, args...)
	}
	client := &hosts.Client{}
	prefix := client.ArgvPrefix(*remote)
	cmd := hosts.RemoteCommandEnv(append([]string{bin}, args...), remote.Cfg.Cwd, envPairs(remote.Cfg.Env))
	full := append(prefix, cmd)
	return exec.CommandContext(ctx, full[0], full[1:]...)
}

// envPairs renders a host's env map as KEY=VALUE pairs (sorted by the caller's
// map iteration being irrelevant — RemoteCommandEnv quotes each).
func envPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// paseoCmd is the Dispatcher's exec seam for the paseo CLI.
func (d *Dispatcher) paseoCmd(ctx context.Context, args ...string) *exec.Cmd {
	return paseoCommand(ctx, d.PaseoBin, d.Remote, args...)
}

// remote reports whether this dispatcher drives a paseo on another box. Local
// filesystem fast-paths (stale-lock clearing, git revalidation of memoized
// checkouts, the $HOME fallback detection, open-workspace adoption) are
// skipped when it does — those inspect paths that only exist on the remote
// side; the paseo CLI remains the source of truth there.
func (d *Dispatcher) remote() bool { return d.Remote != nil }

// paseoCmd is the Reaper's exec seam for the paseo CLI (mirrors the
// Dispatcher's — one reaper runs per paseo runtime, local or remote).
func (r *Reaper) paseoCmd(ctx context.Context, args ...string) *exec.Cmd {
	return paseoCommand(ctx, r.PaseoBin, r.Remote, args...)
}
