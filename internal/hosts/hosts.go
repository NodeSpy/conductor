// Package hosts runs scripts on named SSH targets ("hosts:" entries and inline
// "ssh:" blocks in code/command steps) by shelling out to the system `ssh`
// binary — the same approach internal/handoff/tunnel.go uses for its ssh
// tunnel provider, and the one that needs the least explaining: conductor's
// operator already has working ssh (keys, agent, known_hosts, jump hosts,
// ProxyCommand, ~/.ssh/config host aliases…) configured on the box, and
// shelling out inherits all of it for free instead of us re-implementing a
// chunk of the SSH protocol (and its auth/host-key trust model) with a Go
// client library. BatchMode=yes is load-bearing: it turns any situation that
// would otherwise prompt (password auth offered, an unknown host key with
// strict checking on, …) into an immediate, script-friendly failure instead
// of a hung process waiting on a TTY that will never come.
//
// A Target is a resolved host — either a named `hosts:` entry (Name set) or
// an inline `ssh:` block on a step (Name ""). Both carry the same
// config.HostConfig fields (address, user, port, key, known_hosts, cwd, env),
// so the wrapping/execution logic below doesn't need to know which one it
// got.
package hosts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// sshConnectionErrorExit is the exit code OpenSSH's client reserves for its
// own connection-level failures (DNS/refused/auth/host-key mismatch/timeout)
// as opposed to the remote command's own exit status. Any other code is the
// *remote command's* exit status and is not, by itself, an error — code
// steps and callers are expected to inspect it themselves.
const sshConnectionErrorExit = 255

// Target is a resolved SSH target: a named `hosts:` entry or an inline `ssh:`
// block on a step. Name is "" for the inline form (there is no shared name to
// report); Cfg carries the connection details either way.
type Target struct {
	Name string
	Cfg  config.HostConfig
}

// label returns how a Target should be named in error messages: its
// configured name, falling back to the bare address for inline `ssh:`
// targets (which have no name) so the message still points at *something*
// the operator recognizes from their config.
func (t Target) label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.Cfg.Host
}

// Result is one remote execution's outcome. ExitCode is the remote command's
// exit status (0 on success); it is meaningful even when err is nil — err is
// reserved for failures that mean the command never ran to completion at all
// (connection refused, host key mismatch, ssh binary missing, ctx cancelled).
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runFunc is the injectable local-process seam: given the ssh argv and the
// bytes to feed its stdin, actually run it and report what happened. The
// zero Client uses runLocal (real exec.CommandContext); tests substitute a
// fake that either asserts on argv/stdin directly or (for the "does the
// wrapper actually work" tests) re-execs the trailing remote-command
// argument through a local shell instead of opening a real SSH connection.
type runFunc func(ctx context.Context, argv []string, stdin []byte) (stdout, stderr string, exitCode int, err error)

// Client runs scripts on Targets over SSH. The zero value is usable: SSHBin
// defaults to "ssh" and Run defaults to the real subprocess-exec
// implementation. Both fields are exported so tests (and callers with an
// unusual ssh install) can override them.
type Client struct {
	// SSHBin is the ssh executable to invoke. Empty means "ssh" (resolved via
	// PATH by exec.Command the normal way).
	SSHBin string
	// Run is the injectable local process seam described on runFunc. nil
	// means the real implementation.
	Run runFunc
}

func (c *Client) sshBin() string {
	if c.SSHBin != "" {
		return c.SSHBin
	}
	return "ssh"
}

func (c *Client) run() runFunc {
	if c.Run != nil {
		return c.Run
	}
	return runLocal
}

// Args builds the ssh argv for running remote on t: BatchMode (never
// prompt), then the target's own connection properties in a fixed order so
// output is deterministic and easy to assert on in tests, then `--` (so a
// remote command that happens to start with `-` is never misread as an ssh
// flag) and the command itself.
//
//	ssh -o BatchMode=yes [-p PORT] [-i KEY]
//	    [-o UserKnownHostsFile=KH -o StrictHostKeyChecking=yes]
//	    [USER@]HOST -- <remote command>
//
// KnownHosts also flips on strict host-key checking: an explicit
// known_hosts file is a deliberate pinning decision, and pairing it with
// strict checking is what makes the pin actually enforce anything (without
// it ssh would still fall back to silently trusting an unrecognized key
// interactively — which BatchMode would instead just refuse, but sourcing
// the operator's *intent* from KnownHosts being set is clearer than
// contriving a separate config knob for it).
func (c *Client) Args(t Target, remote string) []string {
	cfg := t.Cfg
	argv := []string{c.sshBin(), "-o", "BatchMode=yes"}
	if cfg.Port != 0 {
		argv = append(argv, "-p", strconv.Itoa(cfg.Port))
	}
	if cfg.Key != "" {
		argv = append(argv, "-i", cfg.Key)
	}
	if cfg.KnownHosts != "" {
		argv = append(argv, "-o", "UserKnownHostsFile="+cfg.KnownHosts, "-o", "StrictHostKeyChecking=yes")
	}
	host := cfg.Host
	if cfg.User != "" {
		host = cfg.User + "@" + host
	}
	argv = append(argv, host, "--", remote)
	return argv
}

// Script executes a POSIX sh script on t with stdin attached, returning its
// stdout/stderr/exit code. env is merged over the target's own Env (the
// call's env wins on key collisions — a one-off override shouldn't require
// editing the shared `hosts:` entry); cwd "" falls back to the target's Cwd.
//
// The merge happens *inside* the remote shell, not by templating values into
// the ssh argv: a wrapper script is built as `export K=V; ...; cd DIR && ` in
// front of the caller's script, then the whole thing is handed to the remote
// shell as a single `sh -c '<wrapper+script>'` argument (see remote()). That
// keeps every value — including ones with secrets in them — off of argv
// (visible to `ps` on both ends of the connection) and out of the shell
// history/logs a naive `ssh host "export K=$V"` string-concat would produce;
// shQuote is what makes embedding arbitrary values into that one shell string
// safe.
func (c *Client) Script(ctx context.Context, t Target, script string, stdin []byte, env map[string]string, cwd string) (Result, error) {
	if cwd == "" {
		cwd = t.Cfg.Cwd
	}
	merged := make(map[string]string, len(t.Cfg.Env)+len(env))
	for k, v := range t.Cfg.Env {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}

	remote := "sh -c " + shQuote(wrapScript(merged, cwd, script))
	argv := c.Args(t, remote)

	stdout, stderr, exitCode, err := c.run()(ctx, argv, stdin)
	res := Result{Stdout: stdout, Stderr: stderr, ExitCode: exitCode}
	if err != nil {
		return res, fmt.Errorf("hosts: %s: %w", t.label(), err)
	}
	if exitCode == sshConnectionErrorExit {
		return res, fmt.Errorf("hosts: %s: ssh connection failed (exit 255): %s", t.label(), strings.TrimSpace(stderr))
	}
	return res, nil
}

// wrapScript builds the remote shell text that Script's env/cwd contract
// executes *inside* the remote sh: exported vars (sorted, for deterministic
// output/tests) first, then a `cd` (only if cwd is set — an empty, unquoted
// `cd` target would just fail), then the caller's script.
func wrapScript(env map[string]string, cwd, script string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s; ", k, shQuote(env[k]))
	}
	if cwd != "" {
		fmt.Fprintf(&b, "cd %s && ", shQuote(cwd))
	}
	b.WriteString(script)
	return b.String()
}

// shQuote single-quotes s for POSIX sh, the one escaping rule that's actually
// safe for arbitrary bytes (including newlines and other shell metacharacters
// secrets tend to contain): wrap in '...', and for every literal single quote
// in s, close the quote, emit an escaped quote, and reopen — the standard
// close/escape/reopen trick. Never use double quotes or backslash-escaping
// here; both still leave `$`, backticks, etc. live.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin single-quotes every element of argv (via shQuote) and joins them
// with spaces, producing one POSIX-sh-safe command line. It is the building
// block for the controller package's "remote controller" wiring: a subprocess
// launcher that would normally exec(argv) locally instead hands
// ShellJoin(argv) to ArgvPrefix's ssh invocation as a single, already-quoted
// remote-command argument.
func ShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shQuote(a)
	}
	return strings.Join(parts, " ")
}

// ArgvPrefix returns the ssh argv for running a command on t, minus the
// trailing remote-command argument itself — i.e. everything Args builds up to
// and including the "--" separator. Exactly one further element must be
// appended to use it: a single pre-quoted command string (ShellJoin(argv), or
// the output of RemoteCommand/RemoteCommandEnv). This split exists so a caller
// that already has its own argv (a controller's launch command) can wrap it
// for remote execution without re-deriving Args' per-target flag ordering.
//
// Per-element quoting must already be applied before appending: ssh
// concatenates however many trailing arguments it's given into one string
// with a single space between them before handing the result to the remote
// shell, so passing one already-assembled, already-quoted string is
// equivalent to passing the original argv unquoted as separate ssh
// arguments — except ssh's own concatenation does no quoting of its own, so
// the caller must have done it (ShellJoin does).
func (c *Client) ArgvPrefix(t Target) []string {
	full := c.Args(t, "")
	return full[:len(full)-1]
}

// RemoteCommand builds the single remote-command string an ArgvPrefix caller
// appends: argv joined via ShellJoin, preceded by a `cd <cwd> &&` when cwd is
// non-empty. Unlike Client.Script (which always executes through an explicit
// `sh -c '...'`), this string is handed to ssh directly — ssh itself invokes
// the remote user's default shell on it, so no `sh -c` wrapper is needed here.
func RemoteCommand(argv []string, cwd string) string {
	cmd := ShellJoin(argv)
	if cwd != "" {
		return "cd " + shQuote(cwd) + " && " + cmd
	}
	return cmd
}

// RemoteCommandEnv extends RemoteCommand with an export preamble: each env
// entry (a "KEY=VALUE" string, e.g. as built by dispatch.AgentEnv) becomes
// `export KEY=<shQuoted VALUE>; ` ahead of the cd/exec. This is how a
// subprocess-seam controller's identity/token env reaches a remote launch: a
// local argv-prefix (ssh ... --) carries no per-process environment across
// the connection, so the KEY=VALUE pairs are instead embedded, quoted, into
// the one remote command string ssh executes — riding the (already
// encrypted) ssh channel instead of appearing in this box's local process
// argv/environment table. Malformed entries (no "=") are skipped. Only the
// given env is exported — os.Environ() is deliberately never consulted here;
// callers that want the target host's own ambient environment get it for
// free (the remote shell already has one), and forwarding this process's
// local environment across the connection is never appropriate.
func RemoteCommandEnv(argv []string, cwd string, env []string) string {
	var b strings.Builder
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "export %s=%s; ", k, shQuote(v))
	}
	b.WriteString(RemoteCommand(argv, cwd))
	return b.String()
}

// runLocal is the real runFunc: exec.CommandContext with separate
// stdout/stderr buffers. A non-zero remote exit surfaces as *exec.ExitError,
// which is expected, ordinary control flow here (the process ran; it just
// didn't return 0) — it is unpacked into ExitCode with err == nil. Only a
// failure that means the process never produced an exit status at all (ssh
// missing from PATH, ctx cancelled/timed out, …) is reported as err.
func runLocal(ctx context.Context, argv []string, stdin []byte) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	if runErr == nil {
		return stdout, stderr, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return stdout, stderr, exitErr.ExitCode(), nil
	}
	return stdout, stderr, -1, runErr
}
