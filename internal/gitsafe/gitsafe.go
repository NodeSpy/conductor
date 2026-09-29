// Package gitsafe is the one place every git invocation conductor makes
// against a base clone or a dispatch worktree gets hardened before it runs.
//
// A base clone's .git/config, and anything a worktree inherits from it, is
// not fully trusted input: it came from a remote conductor does not control
// the far side of (a fork, a PR from an untrusted contributor), and git
// config has several settings that turn "run a git command" into "run
// arbitrary code as the conductor daemon's user":
//
//   - core.hooksPath / a checked-out hooks/ dir — a hook script git runs on
//     checkout, commit, etc.
//   - core.fsmonitor — an arbitrary command git runs on every status-shaped
//     query when set to a path.
//   - protocol.ext.allow / a "ext::" remote URL — runs an arbitrary command
//     as a transport.
//   - diff.external / GIT_EXTERNAL_DIFF — runs an arbitrary command in place
//     of git's own diff.
//   - core.sshCommand — substitutes the transport git uses for an ssh://
//     remote; legitimately operator-configurable, but only from config the
//     operator actually controls, never from the repo being operated on.
//
// Args and Command prepend the -c overrides that neutralize the first four
// unconditionally, and resolve core.sshCommand once from TRUSTED scopes only
// (git's --global then --system config) — never from the repo's own local
// config, so a tampered clone cannot choose what ssh command runs on the
// operator's behalf.
package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// trustedSSH caches the once-resolved trusted core.sshCommand, so every
// invocation in the process shares one resolution rather than re-shelling out
// to `git config` per call.
var trustedSSH struct {
	once sync.Once
	cmd  string
}

// ResetForTest clears the cached trusted-sshCommand resolution. Tests that
// point HOME/GIT_CONFIG_GLOBAL at a fixture must call this first, or they
// will observe whichever process-lifetime value resolved earlier.
func ResetForTest() {
	trustedSSH.once = sync.Once{}
	trustedSSH.cmd = ""
}

// trustedSSHCommand resolves core.sshCommand from the operator's OWN config —
// `git config --global --get`, else `--system --get`, else the plain "ssh"
// default — and deliberately never reads local (repo) config: that would let
// a cloned repo's .git/config (attacker-controlled: a fork, a PR branch)
// choose the command git shells out to for every ssh transport.
func trustedSSHCommand() string {
	trustedSSH.once.Do(func() {
		for _, scope := range []string{"--global", "--system"} {
			out, err := exec.Command("git", "config", scope, "--get", "core.sshCommand").Output()
			if err == nil {
				if v := strings.TrimSpace(string(out)); v != "" {
					trustedSSH.cmd = v
					return
				}
			}
		}
		trustedSSH.cmd = "ssh"
	})
	return trustedSSH.cmd
}

// diffShaped are the subcommands that also need --no-ext-diff appended:
// diff.external= alone stops the config-driven diff driver, but does not
// override GIT_EXTERNAL_DIFF or a driver set via `diff.<driver>.command` for
// a path with a `diff=<driver>` gitattribute — --no-ext-diff is the flag that
// forces git's own internal diff regardless of any of those.
var diffShaped = map[string]bool{"diff": true, "show": true, "log": true, "range-diff": true}

// Args returns args with the hardening `-c` overrides prepended, and
// --no-ext-diff appended for a diff-shaped subcommand. Use this directly when
// a caller assembles its own command line (e.g. wrapping it for an ssh/host
// launch) instead of getting an *exec.Cmd from Command.
func Args(args ...string) []string {
	full := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.sshCommand=" + trustedSSHCommand(),
		"-c", "protocol.ext.allow=never",
		"-c", "diff.external=",
	}
	full = append(full, args...)
	if len(args) > 0 && diffShaped[args[0]] {
		full = append(full, "--no-ext-diff")
	}
	return full
}

// Command builds a hardened `git` *exec.Cmd: -C dir when dir is non-empty
// (run in the current directory otherwise), the Args() hardening flags, and
// GIT_TERMINAL_PROMPT=0 so a missing credential fails fast instead of
// blocking the daemon on an interactive prompt. The caller still sets
// Stdout/Stderr and may append further env.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	full := Args(args...)
	if dir != "" {
		full = append([]string{"-C", dir}, full...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}
