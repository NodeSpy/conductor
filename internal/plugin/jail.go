package plugin

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/scratch"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The declared-capability jail (docs/wiki/Plugins.md "What a plugin
// declares"). Every plugin runs jailed to exactly what it declares, resolved
// against the instances its process serves; anything undeclared is denied.
// Conductor adds nothing a declaration does not name: no destination from a
// config key's name, no login directory for a CLI, no relay for a protocol.
// A plugin that needs more fails with the denial named, and the fix is its
// declaration. `trust: full` is the one opt-out.
//
// What the jail is, per OS:
//
//   - Linux: user, pid, mount, IPC and network namespaces; pivot_root into
//     the minimal base (sandbox.BaseMinimal: /usr's libraries and data but no
//     programs on the PATH directories, a curated /etc, no /opt) plus the
//     plugin's own install dir (read-only), its staging dir (TMPDIR), the
//     declared paths, and the declared commands bound one by one. Everything
//     writable is noexec. The network namespace has loopback only: TCP out
//     goes through conductor's egress proxy (allowlist = declared egress) via
//     HTTP(S)_PROXY, a declared loopback destination through a relay, and a
//     declared listen address is listened on by conductor on the host and
//     piped in.
//   - macOS: a deny-default Seatbelt profile (sandbox.PluginProfile) with the
//     same allow-list: exec of the plugin binary and declared commands only,
//     reads of the OS dirs and declared paths, writes of the declared paths,
//     and network to the egress proxy's loopback port, the declared loopback
//     ports and the declared listen ports only.

// jailHome is the user's home, for "~/" entries (a test var).
var jailHome = os.UserHomeDir

// pluginGrant resolves s's declaration into what its jail allows, narrowed
// by the config (network:, allow_env), never widened by it.
func pluginGrant(s Spec) (sdk.Grant, error) {
	home, _ := jailHome()
	caps := s.Manifest.Caps()
	g, err := caps.Resolve(sdk.ResolveEnv{Home: home, Getenv: os.LookupEnv, Conns: s.Conns, Command: func(n string) (string, bool) {
		p, err := spawnLookPath(n)
		if err != nil {
			return "", false
		}
		real, err := filepath.EvalSymlinks(p)
		return real, err == nil
	}})
	if err != nil {
		return sdk.Grant{}, fmt.Errorf("plugin %s: %w", s.Name, err)
	}
	if s.Probe {
		// A type-level describe serves no instance: no network, no env.
		g.Egress, g.Listen, g.Env = nil, nil, nil
		return g, nil
	}
	if len(s.Network) > 0 {
		var narrowed []string
		for _, n := range s.Network {
			hp, err := normHostPort(n)
			if err != nil {
				return sdk.Grant{}, fmt.Errorf("plugin %s: network: %w", s.Name, err)
			}
			if !egressCovered(g.Egress, hp) {
				return sdk.Grant{}, fmt.Errorf("plugin %s: network: %s is not among what it declares (%s) — a config narrows a plugin's network, never widens it", s.Name, hp, strings.Join(g.Egress, ", "))
			}
			narrowed = append(narrowed, hp)
		}
		g.Egress = narrowed
	}
	if len(s.AllowEnv) > 0 {
		var env []string
		for _, n := range g.Env {
			for _, a := range s.AllowEnv {
				if a == n {
					env = append(env, n)
				}
			}
		}
		g.Env = env
	}
	return g, nil
}

func normHostPort(s string) (string, error) {
	s = strings.TrimSpace(s)
	if h, p, err := net.SplitHostPort(s); err == nil {
		return net.JoinHostPort(h, p), nil
	}
	if s == "" || strings.Contains(s, ":") {
		return "", fmt.Errorf("%q is not host[:port]", s)
	}
	return net.JoinHostPort(s, "443"), nil
}

// loopbackHost: a declared destination on this machine.
func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// splitLoopback separates loopback destinations (reached without the proxy:
// a relay on Linux, a direct connection on macOS) from the rest.
func splitLoopback(egress []string) (remote []string, loopPorts []int) {
	seen := map[int]bool{}
	for _, e := range egress {
		h, p, err := net.SplitHostPort(e)
		if err != nil || !loopbackHost(h) {
			remote = append(remote, e)
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && !seen[n] {
			seen[n] = true
			loopPorts = append(loopPorts, n)
		}
	}
	return remote, loopPorts
}

// jailCommand is one declared command found on this machine.
type jailCommand struct {
	name string
	path string // where the daemon's PATH finds it, directories resolved
	real string // the file it resolves to
}

// findCommands locates the declared commands on the daemon's PATH. One that
// is not installed is left out (the plugin's own call fails, naming it) and
// logged.
func findCommands(s Spec, names []string) []jailCommand {
	var out []jailCommand
	for _, n := range names {
		p, err := spawnLookPath(n)
		if err != nil {
			log.Printf("plugin %s: declared command %s is not installed here", s.Name, n)
			continue
		}
		p, _ = filepath.Abs(p)
		dir, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		out = append(out, jailCommand{name: n, path: filepath.Join(dir, filepath.Base(p)), real: real})
		if spawnGOOS == "darwin" {
			// A macOS /usr/bin developer tool is a stub that runs the real
			// one from the active developer directory: the stub is what the
			// plugin declared, the tool it runs is part of it.
			if dev, err := xcrunFind(n); err == nil && dev != "" && dev != real {
				out = append(out, jailCommand{name: n, path: dev, real: dev})
			}
		}
	}
	return out
}

// xcrunFind is `xcrun --find <tool>` on macOS (a test var).
var xcrunFind = func(tool string) (string, error) {
	if _, err := os.Stat("/usr/bin/xcrun"); err != nil {
		return "", err
	}
	out, err := exec.Command("/usr/bin/xcrun", "--find", tool).Output() //nolint:gosec // fixed binary, tool is a declared command name
	return strings.TrimSpace(string(out)), err
}

// jailPATH is the PATH a jailed plugin runs with: the standard directories
// (which hold only its declared commands) and the directory of any declared
// command found elsewhere.
func jailPATH(cmds []jailCommand) string {
	dirs := []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin"}
	for _, c := range cmds {
		d := filepath.Dir(c.path)
		found := false
		for _, x := range dirs {
			found = found || x == d
		}
		if !found {
			dirs = append(dirs, d)
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// existing keeps the paths that exist (a declared path that does not is
// skipped, as the declaration says).
func existing(ps []string) []string {
	var out []string
	for _, p := range ps {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// buildJailed builds the jailed launch of argv for s: the wrapped command,
// its environment, and what to run when it exits.
func buildJailed(s Spec, sd SandboxDeps, argv, env []string) (*exec.Cmd, func(), error) {
	g, err := pluginGrant(s)
	if err != nil {
		return nil, nil, err
	}
	env = withDeclaredEnv(env, g.Env)
	cmds := findCommands(s, g.Commands)
	env = replaceEnv(env, "PATH", jailPATH(cmds))
	remote, loopPorts := splitLoopback(g.Egress)
	var stage string
	if sd.StagingRoot != "" {
		stage = filepath.Join(sd.StagingRoot, s.Name)
		if err := os.MkdirAll(stage, 0o700); err != nil {
			return nil, nil, fmt.Errorf("plugin %s: staging dir: %w", s.Name, err)
		}
	}
	if spawnGOOS == "darwin" {
		return buildSeatbelt(s, sd, g, argv, env, cmds, remote, loopPorts, stage)
	}
	if spawnGOOS != "linux" {
		return nil, nil, fmt.Errorf("plugin %s: no plugin jail on %s — set trust: full to run it unjailed", s.Name, spawnGOOS)
	}
	return buildNamespaced(s, sd, g, argv, env, cmds, remote, loopPorts, stage)
}

func buildNamespaced(s Spec, sd SandboxDeps, g sdk.Grant, argv, env []string, cmds []jailCommand, remote []string, loopPorts []int, stage string) (*exec.Cmd, func(), error) {
	spec := &sandbox.Spec{Mode: "namespace", Deny: true}
	if err := spec.Check(spawnGOOS, os.Geteuid(), spawnLookPath); err != nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it here (%v) — enable unprivileged user namespaces, or set trust: full to run it unjailed", s.Name, err)
	}
	if sd.Self == "" {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: conductor's own binary (the jail's entry) is not wired", s.Name)
	}
	var binds []sandbox.BindMount
	if s.BinPath != "" {
		binds = append(binds, sandbox.BindMount{Path: filepath.Dir(s.BinPath), RO: true})
	}
	if stage != "" {
		binds = append(binds, sandbox.BindMount{Path: stage, NoExec: true})
	}
	for _, p := range existing(g.Read) {
		binds = append(binds, sandbox.BindMount{Path: p, RO: true, Optional: true})
	}
	for _, p := range existing(g.Write) {
		binds = append(binds, sandbox.BindMount{Path: p, NoExec: true, Optional: true})
	}
	for _, c := range cmds {
		binds = append(binds, sandbox.BindMount{Path: c.path, Src: c.real, RO: true})
	}
	nf := &sandbox.NetForward{Self: sd.Self, Base: sandbox.BaseMinimal}
	var closers []func()
	cleanup := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	// Every launch gets the proxy: with nothing declared its allowlist is
	// empty and it refuses every destination, naming it.
	if sd.EgressUnix == nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: no egress proxy is wired", s.Name)
	}
	sock, cred, revoke, err := sd.EgressUnix(remote)
	if err != nil {
		return nil, nil, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
	}
	closers = append(closers, revoke)
	nf.UnixSocket = sock
	// The forwarder dials the socket after the pivot: the socket itself,
	// read-only (connecting needs no write), not its directory of others.
	binds = append(binds, sandbox.BindMount{Path: sock, RO: true})
	env = append(env, sandbox.ProxyEnv(sandbox.ForwardAddr, cred)...)

	if len(loopPorts) > 0 || len(g.Listen) > 0 {
		dir, err := os.MkdirTemp(scratch.SystemTempDir(), "cpj")
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("plugin %s: jail sockets: %w", s.Name, err)
		}
		closers = append(closers, func() { os.RemoveAll(dir) })
		for i, port := range loopPorts {
			path := filepath.Join(dir, "r"+strconv.Itoa(i))
			stop, err := serveUnixToTCP(path, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("plugin %s: relay for localhost:%d: %w", s.Name, port, err)
			}
			closers = append(closers, stop)
			nf.Relays = append(nf.Relays, sandbox.Relay{Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Unix: path})
			binds = append(binds, sandbox.BindMount{Path: path, RO: true})
		}
		for i, addr := range g.Listen {
			_, port, _ := net.SplitHostPort(addr)
			path := filepath.Join(dir, "l"+strconv.Itoa(i))
			stop, err := serveTCPToUnix(addr, path)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("plugin %s: listen %s: %w", s.Name, addr, err)
			}
			closers = append(closers, stop)
			nf.Exposes = append(nf.Exposes, sandbox.Expose{Unix: path, Dial: net.JoinHostPort("127.0.0.1", port)})
		}
	}
	nf.Binds = binds
	wrapped, err := spec.WrapLocal(argv, "", envKeys(env), nf)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("plugin %s: jail: %w", s.Name, err)
	}
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, jailed
	c.Env = env
	return c, cleanup, nil
}

func buildSeatbelt(s Spec, sd SandboxDeps, g sdk.Grant, argv, env []string, cmds []jailCommand, remote []string, loopPorts []int, stage string) (*exec.Cmd, func(), error) {
	if _, err := spawnLookPath("sandbox-exec"); err != nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it here (no sandbox-exec) — set trust: full to run it unjailed", s.Name)
	}
	home, _ := jailHome()
	p := &sandbox.PluginProfile{Keychain: g.Keychain, Home: home, LoopbackPorts: loopPorts}
	if s.BinPath != "" {
		p.Exec = append(p.Exec, s.BinPath)
		p.Read = append(p.Read, filepath.Dir(s.BinPath))
	}
	for _, c := range cmds {
		p.Exec = append(p.Exec, c.path, c.real)
	}
	p.Read = append(p.Read, existing(g.Read)...)
	p.Write = append(p.Write, existing(g.Write)...)
	if stage != "" {
		p.Write = append(p.Write, stage)
	}
	for _, addr := range g.Listen {
		_, port, _ := net.SplitHostPort(addr)
		if n, err := strconv.Atoi(port); err == nil {
			p.ListenPorts = append(p.ListenPorts, n)
		}
	}
	cleanup := func() {}
	if sd.EgressAddr == nil {
		return nil, nil, fmt.Errorf("plugin %s: cannot jail it: no egress proxy is wired", s.Name)
	}
	addr, cred, revoke, err := sd.EgressAddr(remote)
	if err != nil {
		return nil, nil, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
	}
	cleanup = revoke
	_, port, _ := net.SplitHostPort(addr)
	p.ProxyPort, _ = strconv.Atoi(port)
	env = append(env, sandbox.ProxyEnv(addr, cred)...)
	wrapped := sandbox.WrapSeatbeltPlugin(argv, p)
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, jailed
	c.Env = env
	return c, cleanup, nil
}

// serveUnixToTCP listens on a unix socket and pipes each connection to a
// TCP address on this machine: a declared loopback destination, reached
// from a jail with no network of its own.
func serveUnixToTCP(path, addr string) (func(), error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	go pipeAll(ln, func() (net.Conn, error) { return net.Dial("tcp", addr) })
	return func() { ln.Close() }, nil
}

// serveTCPToUnix listens on a declared TCP address on the host and pipes
// each connection to the jail's exposed socket at path.
func serveTCPToUnix(addr, path string) (func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go pipeAll(ln, func() (net.Conn, error) { return net.Dial("unix", path) })
	return func() { ln.Close() }, nil
}

func pipeAll(ln net.Listener, dial func() (net.Conn, error)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			u, err := dial()
			if err != nil {
				return
			}
			defer u.Close()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(u, c)
				if cw, ok := u.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
			}()
			_, _ = io.Copy(c, u)
			if cw, ok := c.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			wg.Wait()
		}(c)
	}
}
