package agentsandbox

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Policy is one agent launch's sandbox: who it is, what it may see and
// reach. The daemon builds it (Resolver; the `sandbox-policy` control
// command for `conductor sandbox exec`); Wrap realizes it. It travels as JSON
// over the control socket, so every field is plain data.
type Policy struct {
	// Identity names who the agent is: a dispatch's profile, a member, or —
	// for a directory conductor did not create — the directory itself.
	Identity string `json:"identity"`
	// Tool is the canonical agent CLI (CanonicalTool): which state, login
	// and model endpoints apply.
	Tool string `json:"tool,omitempty"`
	// Workspace is the agent's workspace, read-write.
	Workspace string `json:"workspace"`
	// Cwd is where the agent starts, inside Workspace ("" = Workspace).
	Cwd string `json:"cwd,omitempty"`
	// Home is the identity's tool-state directory, the agent's HOME,
	// read-write; it exists before the launch (PrepareHome).
	Home string `json:"home"`
	// Logins are the operator's login files shared into Home.
	Logins []Login `json:"logins,omitempty"`
	// Egress is the proxy allowlist (host, host:port, *.domain). Empty means
	// no network at all.
	Egress []string `json:"egress,omitempty"`
	// Loopback are host-loopback model endpoints ("127.0.0.1:3456") relayed
	// into the sandbox at the same address.
	Loopback []string `json:"loopback,omitempty"`
	// Expose are in-sandbox loopback addresses ("127.0.0.1:4096") a server
	// the agent runs listens on, which the launcher connects to (opencode's
	// `opencode serve`): Launch.Exposed says how.
	Expose []string `json:"expose,omitempty"`
	// Socket is the conductor skill socket (the file, bound alone — never
	// its directory, which is conductor's state) and Token this launch's
	// freshly minted session token for it.
	Socket string `json:"socket,omitempty"`
	Token  string `json:"token,omitempty"`
	// ReadOnly / ReadWrite are further paths the workspace needs: a
	// per-dispatch clone's borrowed object store (read-only), a linked
	// worktree's git dir (read-write).
	ReadOnly  []string `json:"read_only,omitempty"`
	ReadWrite []string `json:"read_write,omitempty"`
	// Hide are directories inside a read-write path shown empty (a linked
	// worktree's siblings); a ReadWrite path deeper inside stays visible.
	Hide []string `json:"hide,omitempty"`
	// Env are extra KEY=VALUE variables for the agent (its git identity).
	Env []string `json:"env,omitempty"`
	// Note is something the launcher should tell its operator about this
	// policy (a scratch workspace in place of a refused directory).
	Note string `json:"note,omitempty"`
	// Container runs the agent in a container instead of the
	// namespace/Seatbelt jail (isolation `mode: container`; nil = the jail).
	Container *Container `json:"container,omitempty"`
}

// Validate refuses a policy that cannot be realized as a boundary.
func (p Policy) Validate() error {
	if !filepath.IsAbs(p.Workspace) {
		return fmt.Errorf("agentsandbox: workspace %q is not an absolute path", p.Workspace)
	}
	if fi, err := os.Stat(p.Workspace); err != nil || !fi.IsDir() {
		return fmt.Errorf("agentsandbox: workspace %s is not a directory", p.Workspace)
	}
	if p.Cwd != "" && !within(p.Cwd, p.Workspace) {
		return fmt.Errorf("agentsandbox: cwd %s is outside the workspace %s", p.Cwd, p.Workspace)
	}
	if !filepath.IsAbs(p.Home) {
		return fmt.Errorf("agentsandbox: identity home %q is not an absolute path", p.Home)
	}
	if fi, err := os.Stat(p.Home); err != nil || !fi.IsDir() {
		// A bind source that does not exist would leave the agent writing
		// into an ephemeral mount: its state lost at exit.
		return fmt.Errorf("agentsandbox: identity home %s does not exist (it must be created before the launch)", p.Home)
	}
	if p.Socket != "" {
		if fi, err := os.Stat(p.Socket); err != nil || fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("agentsandbox: conductor socket %s is not a socket", p.Socket)
		}
	}
	for _, e := range p.Egress {
		if e == "" {
			return fmt.Errorf("agentsandbox: empty egress pattern")
		}
	}
	for _, lb := range p.Loopback {
		h, port, err := net.SplitHostPort(lb)
		if err != nil || !isLoopbackHost(h) || port == "" {
			return fmt.Errorf("agentsandbox: relay %q is not a loopback host:port", lb)
		}
	}
	for _, x := range p.Expose {
		h, port, err := net.SplitHostPort(x)
		if n, perr := strconv.Atoi(port); err != nil || !isLoopbackHost(h) || perr != nil || n <= 0 || n > 65535 {
			return fmt.Errorf("agentsandbox: exposed address %q is not a loopback host:port", x)
		}
	}
	for _, list := range [][]string{p.ReadOnly, p.ReadWrite, p.Hide} {
		for _, q := range list {
			if !filepath.IsAbs(q) {
				return fmt.Errorf("agentsandbox: path %q is not absolute", q)
			}
		}
	}
	return nil
}

// Deps are the host-side services a sandboxed launch uses.
type Deps struct {
	// SelfExe is conductor's own binary: the in-sandbox forwarder on Linux,
	// and `conductor call` inside the sandbox.
	SelfExe func() (string, error)
	// EgressUnix / EgressAddr mint the allowlist proxy's endpoint: a unix
	// socket the Linux sandbox's forwarder pipes into, a loopback port the
	// macOS profile allows.
	EgressUnix func(allow []string) (sock, cred string, revoke func(), err error)
	EgressAddr func(allow []string) (addr, cred string, revoke func(), err error)
	// Relay opens a host-side unix socket piped to a host-loopback target
	// (Linux; macOS reaches the port directly).
	Relay func(target string) (sock string, closeFn func(), err error)
	// LookPath resolves the launch binary (exec.LookPath).
	LookPath func(string) (string, error)
	// RealHome is the operator's home.
	RealHome string
	// Inherited is the environment the agent's is built from — the
	// launcher's own; AgentEnv keeps only what an agent needs.
	Inherited []string
	// Bins are further programs the launch runs by name — the agent CLI a
	// shell command starts (agent-deck's): each is shown in the sandbox
	// like the launch binary itself, and must resolve here.
	Bins []string
}

// Launch is a wrapped launch: run Argv in Dir with exactly Env (not appended
// to the caller's environment), and call Cleanup when it ends.
type Launch struct {
	Argv    []string
	Env     []string
	Dir     string
	Cleanup func()
	// Exposed maps each Policy.Expose address to the host unix socket that
	// reaches it (Linux: the sandbox has its own network), or to "" where
	// the address itself is reachable from the host (macOS).
	Exposed map[string]string
	// Signal delivers a signal (TERM, KILL, …) to the agent where signalling
	// the launched process does not reach it (a container launch: the
	// engine client forwards none). nil: signal the process.
	Signal func(sig string)
}

// Wrap realizes p around argv. Fail-closed: any error means no launch.
func Wrap(p Policy, argv []string, d Deps) (*Launch, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("agentsandbox: nothing to launch")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Container != nil {
		return WrapContainer(p, argv, d)
	}
	if d.SelfExe == nil || d.LookPath == nil {
		return nil, fmt.Errorf("agentsandbox: launcher not wired")
	}
	goos := sandbox.CheckGOOS
	argv = append([]string(nil), argv...)
	if !filepath.IsAbs(argv[0]) {
		// The sandbox has none of the operator's PATH dirs: exec the binary
		// by the path it resolves to here.
		bin, err := d.LookPath(argv[0])
		if err != nil {
			return nil, fmt.Errorf("agentsandbox: %s: %w", argv[0], err)
		}
		argv[0] = bin
	}
	self, err := d.SelfExe()
	if err != nil {
		return nil, fmt.Errorf("agentsandbox: resolve conductor binary: %w", err)
	}

	dir := p.Workspace
	binds := []sandbox.BindMount{{Path: p.Home}}
	if p.Cwd != "" && p.Cwd != p.Workspace {
		dir = p.Cwd
		binds = append(binds, sandbox.BindMount{Path: p.Workspace})
	}
	prof := &sandbox.AgentProfile{Home: d.RealHome}
	for _, l := range p.Logins {
		// Linux binds the operator's file over the placeholder; on macOS the
		// identity home links to it, and seatbeltBinds allows its source.
		binds = append(binds, sandbox.BindMount{Path: l.Path, Src: l.Src})
	}
	binds = append(binds, launchBinds(argv[0], d.LookPath, d.RealHome)...)
	for _, b := range d.Bins {
		bin, err := d.LookPath(b)
		if err != nil {
			return nil, fmt.Errorf("agentsandbox: %s: %w", b, err)
		}
		binds = append(binds, launchBinds(bin, d.LookPath, d.RealHome)...)
	}
	for _, f := range launcherFiles(argv[1:]) {
		binds = append(binds, sandbox.BindMount{Path: f, RO: true})
	}
	if !visibleInBase(self) {
		// `conductor call` inside the sandbox, at the path the tool server
		// and the skill CLI name it by.
		real := self
		if r, err := filepath.EvalSymlinks(self); err == nil {
			real = r
		}
		binds = append(binds, sandbox.BindMount{Path: self, Src: real, RO: true})
	}
	if p.Socket != "" {
		binds = append(binds, sandbox.BindMount{Path: p.Socket})
		prof.UnixSockets = append(prof.UnixSockets, p.Socket)
	}
	for _, q := range p.ReadOnly {
		binds = append(binds, sandbox.BindMount{Path: q, RO: true})
	}
	for _, q := range p.ReadWrite {
		binds = append(binds, sandbox.BindMount{Path: q})
	}
	for _, q := range p.Hide {
		binds = append(binds, sandbox.BindMount{Path: q, Tmpfs: true})
	}
	if goos == "darwin" {
		// Seatbelt allow rules add up: a read-only path inside a read-write
		// one needs an explicit write denial, a hidden one a denial, and a
		// read-write path inside a hidden one its allow back (later wins).
		for _, q := range p.ReadOnly {
			for _, w := range append([]string{p.Workspace, p.Home}, p.ReadWrite...) {
				if within(q, w) {
					prof.WriteDeny = append(prof.WriteDeny, q)
					break
				}
			}
		}
		prof.Hide = append(prof.Hide, p.Hide...)
		for _, q := range p.ReadWrite {
			for _, h := range p.Hide {
				if within(q, h) {
					prof.Unhide = append(prof.Unhide, q)
					break
				}
			}
		}
	}

	env := AgentEnv(d.Inherited, p)
	if goos == "darwin" {
		// Seatbelt shows no shared temp dir: the identity's own.
		tmp := filepath.Join(p.Home, ".tmp")
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return nil, err
		}
		env = append(env, "TMPDIR="+tmp)
	} else {
		env = append(env, "TMPDIR=/tmp") // the jail's private tmpfs
	}

	spec := &sandbox.Spec{Mode: "namespace", Deny: true, HasEgress: len(p.Egress) > 0, Egress: append([]string(nil), p.Egress...)}
	deps := sandbox.LocalWrapDeps{
		SelfExe:    func() (string, error) { return self, nil },
		Confine:    true,
		EgressUnix: d.EgressUnix,
		EgressAddr: d.EgressAddr,
	}
	var closers []func()
	cleanupAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	exposed := map[string]string{}
	if goos == "darwin" {
		// No network namespace: the model's loopback port is reached
		// directly, the profile allowing exactly it — and a server the agent
		// runs listens on the host's loopback, at exactly its port.
		for _, lb := range p.Loopback {
			_, port, _ := net.SplitHostPort(lb)
			n, _ := strconv.Atoi(port)
			prof.LoopbackPorts = append(prof.LoopbackPorts, n)
		}
		for _, x := range p.Expose {
			_, port, _ := net.SplitHostPort(x)
			n, _ := strconv.Atoi(port)
			prof.ListenPorts = append(prof.ListenPorts, n)
			exposed[x] = ""
		}
		deps.Agent = prof
		deps.ExtraBinds = seatbeltBinds(binds)
	} else {
		for _, lb := range p.Loopback {
			if d.Relay == nil {
				return nil, fmt.Errorf("agentsandbox: model endpoint %s is on the host loopback and no relay is wired", lb)
			}
			sock, closeFn, err := d.Relay(lb)
			if err != nil {
				cleanupAll()
				return nil, fmt.Errorf("agentsandbox: relay %s: %w", lb, err)
			}
			closers = append(closers, closeFn)
			binds = append(binds, sandbox.BindMount{Path: sock})
			deps.Relays = append(deps.Relays, sandbox.Relay{Listen: lb, Unix: sock})
		}
		if len(p.Expose) > 0 {
			// The forwarder creates these sockets before the jail is built,
			// so their directory is never shown to the agent.
			dir, err := SocketDir("cse")
			if err != nil {
				cleanupAll()
				return nil, fmt.Errorf("agentsandbox: expose dir: %w", err)
			}
			closers = append(closers, func() { _ = os.RemoveAll(dir) })
			for i, x := range p.Expose {
				sock := filepath.Join(dir, fmt.Sprintf("e%d.sock", i))
				deps.Exposes = append(deps.Exposes, sandbox.Expose{Unix: sock, Dial: x})
				exposed[x] = sock
			}
		}
		deps.ExtraBinds = binds
	}
	wrapped, outEnv, wclean, err := sandbox.WrapLocalCommand(spec, argv, dir, env, deps)
	if err != nil {
		cleanupAll()
		return nil, err
	}
	closers = append([]func(){wclean}, closers...)
	return &Launch{Argv: wrapped, Env: outEnv, Dir: dir, Cleanup: cleanupAll, Exposed: exposed}, nil
}

// seatbeltBinds turns the Linux allow-list into what a Seatbelt profile can
// express: rules on real paths. A symlink hop needs no rule (the link is in
// the real filesystem; following it is metadata, which the base allows), a
// tmpfs has no analog, and a bind from another source allows that source.
func seatbeltBinds(binds []sandbox.BindMount) []sandbox.BindMount {
	var out []sandbox.BindMount
	for _, b := range binds {
		switch {
		case b.Link != "" || b.Tmpfs:
			continue
		case b.Src != "":
			out = append(out, sandbox.BindMount{Path: b.Src, RO: b.RO})
		default:
			out = append(out, sandbox.BindMount{Path: b.Path, RO: b.RO})
		}
	}
	return out
}
