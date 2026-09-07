// Package sandbox builds per-dispatch isolation wrappers for the runtimes
// conductor launches itself (#36 §15). Three modes, selectable per profile /
// runtime / host, each expressed as an argv (or remote-command) prefix around
// the launch conductor was going to perform anyway:
//
//   - user      — run as a distinct low-privilege OS user (`sudo -n -u <user>`).
//     Works on any Unix with a sudoers rule for the conductor user; the
//     sandbox user shares nothing with the daemon's own account.
//   - namespace — Linux user+pid+mount namespaces via `unshare`, with
//     optional cgroup resource limits via a `systemd-run --user --scope`
//     prefix. `network: {deny: true}` adds a network namespace: the agent
//     has NO network, structurally.
//   - container — `docker run`/`podman run` with the worktree bind-mounted;
//     `network: {deny: true}` becomes `--network=none`, limits become the
//     engine's own flags.
//
// The network egress allowlist is conductor-enforced through the Proxy in
// proxy.go: conductor listens on a loopback port, filters CONNECT/absolute
// requests against the profile's `egress:` patterns, and injects
// HTTP(S)_PROXY into the launched runtime's environment. A well-behaved
// runtime's traffic flows through (and is filtered/audited by) conductor; a
// structural guarantee needs `deny: true` under namespace/container mode.
//
// Everything degrades gracefully off Linux: user/container modes work
// wherever sudo/docker do; namespace mode reports a clear error from Check
// (and is rejected by `conductor validate` on a non-Linux box) instead of
// failing mid-dispatch.
package sandbox

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Spec is a resolved isolation policy — config.IsolationConfig with the
// pointers flattened, ready to wrap a launch.
type Spec struct {
	Mode      string // user | namespace | container
	User      string // mode: user — the low-privilege account
	Image     string // mode: container — the image to run
	Engine    string // mode: container — docker (default) | podman
	Memory    string // cgroup/engine memory cap ("2g"); empty = none
	CPU       string // cgroup CPUQuota ("200%") / engine --cpus ("2"); empty = none
	Pids      int    // max tasks/pids; 0 = none
	Deny      bool   // structural no-network (namespace --net / --network=none)
	Egress    []string
	HasEgress bool // a network: block was present (even empty ⇒ deny-all proxy)
}

// FromConfig flattens an IsolationConfig. nil in, nil out.
func FromConfig(c *config.IsolationConfig) *Spec {
	if c == nil {
		return nil
	}
	s := &Spec{Mode: c.Mode, User: c.User}
	if c.Container != nil {
		s.Image = c.Container.Image
		s.Engine = c.Container.Engine
	}
	if c.Limits != nil {
		s.Memory = c.Limits.Memory
		s.CPU = c.Limits.CPU
		s.Pids = c.Limits.Pids
	}
	if c.Network != nil {
		s.Deny = c.Network.Deny
		s.Egress = c.Network.Egress
		s.HasEgress = true
	}
	return s
}

// engine returns the container engine binary (docker unless podman chosen).
func (s *Spec) engine() string {
	if s.Engine != "" {
		return s.Engine
	}
	return "docker"
}

// ProxyPolicy reports whether this launch's network is governed by the
// egress proxy (an explicit network: block that isn't a structural deny),
// and the allowlist to enforce (empty = deny everything, audited).
func (s *Spec) ProxyPolicy() (allow []string, ok bool) {
	if s == nil || !s.HasEgress || s.Deny {
		return nil, false
	}
	return s.Egress, true
}

// Check verifies the mode is runnable here: the wrapper binaries exist and
// namespace mode is on Linux. goos is runtime.GOOS; lookPath is
// exec.LookPath (both injected for tests). A remote launch (the wrapper runs
// on another box over ssh) should skip Check — the remote box's own PATH and
// OS apply there.
func (s *Spec) Check(goos string, lookPath func(string) (string, error)) error {
	if s == nil {
		return nil
	}
	need := func(bin, why string) error {
		if _, err := lookPath(bin); err != nil {
			return fmt.Errorf("sandbox: isolation mode %s needs %q on PATH (%s): %w", s.Mode, bin, why, err)
		}
		return nil
	}
	switch s.Mode {
	case "user":
		return need("sudo", "to switch to the sandbox user")
	case "namespace":
		if goos != "linux" {
			return fmt.Errorf("sandbox: isolation mode namespace is Linux-only (running on %s) — use mode user or container here", goos)
		}
		if err := need("unshare", "to enter the namespaces"); err != nil {
			return err
		}
		if s.Memory != "" || s.CPU != "" || s.Pids > 0 {
			return need("systemd-run", "to apply cgroup limits")
		}
		return nil
	case "container":
		return need(s.engine(), "to run the container")
	case "":
		return nil
	default:
		return fmt.Errorf("sandbox: unknown isolation mode %q", s.Mode)
	}
}

// WrapLocal wraps a local launch: argv becomes the isolated invocation. dir
// is the working directory (container mode bind-mounts it); envKeys are the
// environment keys the launch will carry (container mode passes each through
// with `-e KEY` since a container never inherits the parent environment).
func (s *Spec) WrapLocal(argv []string, dir string, envKeys []string) ([]string, error) {
	if s == nil {
		return argv, nil
	}
	switch s.Mode {
	case "user":
		if s.User == "" {
			return nil, fmt.Errorf("sandbox: isolation mode user needs `user:`")
		}
		return append([]string{"sudo", "-n", "-u", s.User, "--"}, argv...), nil
	case "namespace":
		prefix := s.systemdPrefix()
		prefix = append(prefix, "unshare", "--user", "--map-current-user",
			"--pid", "--fork", "--mount-proc", "--kill-child")
		if s.Deny {
			prefix = append(prefix, "--net")
		}
		prefix = append(prefix, "--")
		return append(prefix, argv...), nil
	case "container":
		if s.Image == "" {
			return nil, fmt.Errorf("sandbox: isolation mode container needs `container.image:`")
		}
		out := []string{s.engine(), "run", "--rm", "-i"}
		if dir != "" {
			out = append(out, "-v", dir+":"+dir, "-w", dir)
		}
		if s.Deny {
			out = append(out, "--network=none")
		}
		if s.Memory != "" {
			out = append(out, "--memory", s.Memory)
		}
		if s.CPU != "" {
			out = append(out, "--cpus", s.CPU)
		}
		if s.Pids > 0 {
			out = append(out, "--pids-limit", strconv.Itoa(s.Pids))
		}
		for _, k := range envKeys {
			out = append(out, "-e", k)
		}
		out = append(out, s.Image)
		return append(out, argv...), nil
	case "":
		return argv, nil
	default:
		return nil, fmt.Errorf("sandbox: unknown isolation mode %q", s.Mode)
	}
}

// systemdPrefix renders the cgroup-limit scope prefix for namespace mode
// (empty when no limits are set).
func (s *Spec) systemdPrefix() []string {
	if s.Memory == "" && s.CPU == "" && s.Pids == 0 {
		return nil
	}
	out := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect"}
	if s.Memory != "" {
		out = append(out, "-p", "MemoryMax="+s.Memory)
	}
	if s.CPU != "" {
		out = append(out, "-p", "CPUQuota="+s.CPU)
	}
	if s.Pids > 0 {
		out = append(out, "-p", "TasksMax="+strconv.Itoa(s.Pids))
	}
	return out
}

// WrapRemote wraps a remote shell command string (the single argument ssh
// hands the remote shell) in the isolation prefix, re-quoting it through
// `sh -c` so the prefix wrapper execs exactly one command whatever shell
// operators cmd contains. Container mode is rejected here — a remote
// container launch needs mounts and images conductor can't verify from this
// box; configure the isolation on the remote host's own conductor instead.
func (s *Spec) WrapRemote(cmd string) (string, error) {
	if s == nil {
		return cmd, nil
	}
	switch s.Mode {
	case "user":
		if s.User == "" {
			return "", fmt.Errorf("sandbox: isolation mode user needs `user:`")
		}
		return "sudo -n -u " + shQuote(s.User) + " -- sh -c " + shQuote(cmd), nil
	case "namespace":
		prefix := strings.Join(s.systemdPrefix(), " ")
		flags := "--user --map-current-user --pid --fork --mount-proc --kill-child"
		if s.Deny {
			flags += " --net"
		}
		wrapped := "unshare " + flags + " -- sh -c " + shQuote(cmd)
		if prefix != "" {
			wrapped = prefix + " " + wrapped
		}
		return wrapped, nil
	case "container":
		return "", fmt.Errorf("sandbox: isolation mode container is not supported for remote (host:) execution — configure it on the remote box, or use mode user/namespace")
	case "":
		return cmd, nil
	default:
		return "", fmt.Errorf("sandbox: unknown isolation mode %q", s.Mode)
	}
}

// shQuote single-quotes s for POSIX sh (the close/escape/reopen rule —
// mirrors internal/hosts.shQuote).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// EgressAllowed reports whether hostport ("api.example.com:443") matches the
// allowlist. Patterns are "host", "host:port", or globs on the host half
// ("*.example.com", "*.example.com:443"); "*" allows everything. A bare-host
// pattern allows any port on that host. Matching is case-insensitive on the
// host. An empty list allows nothing (deny-by-default).
func EgressAllowed(allow []string, hostport string) bool {
	host, port := splitHostPort(hostport)
	for _, pat := range allow {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if pat == "*" {
			return true
		}
		ph, pp := splitHostPort(pat)
		if pp != "" && pp != port {
			continue
		}
		if ok, err := path.Match(strings.ToLower(ph), strings.ToLower(host)); err == nil && ok {
			return true
		}
	}
	return false
}

// splitHostPort splits "host:port" tolerantly ("" port when absent). IPv6
// literals in brackets keep their colons.
func splitHostPort(s string) (host, port string) {
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end >= 0 {
			host = s[1:end]
			if rest := s[end+1:]; strings.HasPrefix(rest, ":") {
				port = rest[1:]
			}
			return host, port
		}
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i+1:], ":") {
		return s[:i], s[i+1:]
	}
	return s, ""
}
