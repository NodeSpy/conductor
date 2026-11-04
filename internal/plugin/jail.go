package plugin

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The default plugin jail.
//
// Every plugin process — a connector, a runtime plugin, a step engine, the
// type-level describe probe — runs in it unless its entry has an isolation:
// block of its own (that sandbox, fail-closed), trust: full (or isolation
// mode none: no OS sandbox, its manifest still applies), or the operator set
// sandbox.plugins: off. What the jail shows the plugin is assembled here from
// what it declares (egress, commands, fs), what the operator's config points
// it at (connection paths, listen addresses, the entry's fs:), and the
// plugin's own directories; the mechanism is the same one code steps and
// agents use (sandbox.WrapLocalCommand: Linux user/pid/mount/net namespaces
// with a pivot_root allow-list, macOS Seatbelt).

// JailPolicy is sandbox.plugins as the spawn path reads it.
type JailPolicy string

const (
	// JailOff runs plugins without the default jail. It is the zero value, so
	// a caller that does not wire the daemon's sandbox (a test) gets the
	// pre-jail behavior; the daemon always sets the configured policy.
	JailOff JailPolicy = ""
	// JailOn jails every plugin; where this box cannot build the jail, the
	// plugin runs confined to its manifest only, with a warning.
	JailOn JailPolicy = "on"
	// JailRequired jails every plugin and refuses to start one whose jail
	// cannot be built.
	JailRequired JailPolicy = "required"
)

// JailPolicyFor maps the config value (sandbox.plugins) to a JailPolicy.
func JailPolicyFor(s string) JailPolicy {
	switch s {
	case "off":
		return JailOff
	case "required":
		return JailRequired
	}
	return JailOn
}

// Confinement says how one plugin process was launched: the log line at
// spawn and `plugin list --caps` render it.
type Confinement struct {
	// Mode is "jail" (the default jail), "isolation" (the operator's
	// isolation: block), "trusted" (trust: full or isolation mode none),
	// "off" (sandbox.plugins: off), "unavailable" (sandbox.plugins: on,
	// and this box cannot build the jail) or, only as a plan, "refused"
	// (sandbox.plugins: required, and this box cannot build it).
	Mode string
	// Network describes the network the process got.
	Network string
	// Why is the reason for "unavailable".
	Why string
	// Notes are further facts worth a log line (a listen address that could
	// not be forwarded).
	Notes []string
}

// String renders c as the one-line description `plugin list --caps` and the
// spawn log use.
func (c Confinement) String() string {
	switch c.Mode {
	case "jail":
		return "OS jail: filesystem limited to its own dirs, declared paths and commands; network: " + c.Network
	case "isolation":
		return "the isolation: block's sandbox"
	case "trusted":
		return "no OS jail (trust: full); its declared manifest applies"
	case "off":
		return "no OS jail (sandbox.plugins: off); its declared manifest applies"
	case "refused":
		return "not started — its OS jail cannot be built on this box (" + c.Why + ") and sandbox.plugins is required"
	case "unavailable":
		return "no OS jail — it cannot be built on this box (" + c.Why + "); its declared manifest applies"
	}
	return c.Mode
}

// PlannedConfinement is the confinement s would be launched under with sd,
// decided as buildCommand decides it but without building anything — what
// `conductor plugin list --caps` shows. A jail that fails to build at launch
// (a missing bind source, a proxy error) is not foreseen here.
func PlannedConfinement(s Spec, sd SandboxDeps) Confinement {
	switch {
	case s.TrustFull || s.Isolation.OptsOut():
		return Confinement{Mode: "trusted"}
	case sandbox.FromConfig(s.Isolation) != nil:
		return Confinement{Mode: "isolation"}
	case sd.Jail == JailOff && s.Kind != KindStep:
		return Confinement{Mode: "off"}
	}
	if sd.JailProbe != nil {
		if why := sd.JailProbe(); why != "" {
			if sd.Jail == JailRequired {
				return Confinement{Mode: "refused", Why: why}
			}
			return Confinement{Mode: "unavailable", Why: why}
		}
	}
	return Confinement{Mode: "jail", Network: jailNetwork(s).describe()}
}

// jailNet is the network decision for one jailed plugin.
type jailNet struct {
	deny  bool     // its own network namespace (Linux) / outbound cut (macOS)
	allow []string // with deny: the egress proxy's allowlist ("" = no way out)
	why   string   // without deny: why the host network is left open
}

// jailNetwork decides a jailed plugin's network from its effective manifest
// (declared egress, narrowed — or, when it declares none, set — by the
// operator's network:).
//
//   - The describe probe and step engines: their own namespace, with the
//     manifest's egress (usually none) as the only way out.
//   - A plugin with egress: its own namespace; the only way out is
//     conductor's egress proxy with exactly that allowlist.
//   - A plugin with an `exposes` verb (a tunnel or relay that dials a local
//     address it is handed per call): the host network.
//   - A connector or runtime that declares no egress and has no network:
//     the host network. Such plugins (home-lab services, LAN APIs) dial the
//     host the operator configures; there is no allowlist to enforce, and
//     cutting them off would break them. network: confines one.
func jailNetwork(s Spec) jailNet {
	m := s.EffectiveManifest()
	switch {
	case s.Probe, s.Kind == KindStep:
		return jailNet{deny: true, allow: m.Egress}
	case s.hostNetwork || m.Exposes:
		return jailNet{why: "it has an exposes verb (it dials local addresses it is handed)"}
	case len(m.Egress) > 0:
		return jailNet{deny: true, allow: m.Egress}
	}
	return jailNet{why: "it declares no egress and its entry sets no network:"}
}

// describe renders n for Confinement.Network.
func (n jailNet) describe() string {
	switch {
	case n.deny && len(n.allow) > 0:
		return "only through conductor's egress proxy to " + strings.Join(n.allow, ", ")
	case n.deny:
		return "none"
	}
	return "host network, not confined — " + n.why
}

// buildJail wraps argv in the default jail. env is the scrubbed environment
// the plugin would get anyway (base env, granted env, the confined PATH); the
// jail adds the proxy variables and replaces HOME and TMPDIR.
func buildJail(s Spec, sd SandboxDeps, argv, env []string) (wrapped, outEnv []string, cleanup func(), conf Confinement, err error) {
	cleanup = func() {}
	nw := jailNetwork(s)
	conf = Confinement{Mode: "jail", Network: nw.describe()}
	spec := &sandbox.Spec{Mode: "namespace", Deny: nw.deny, HasEgress: nw.deny && len(nw.allow) > 0, Egress: nw.allow}

	var cleanups []func()
	done := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(e error) ([]string, []string, func(), Confinement, error) {
		done()
		return nil, nil, func() {}, conf, e
	}

	binds, home, tmp, err := jailFiles(s, sd)
	if err != nil {
		return fail(err)
	}
	if tmp != "" && spawnGOOS == "darwin" {
		// No private /tmp on macOS: a per-launch scratch dir instead.
		cleanups = append(cleanups, func() { _ = os.RemoveAll(tmp) })
	}
	env = replaceEnv(env, "HOME", home)
	env = replaceEnv(env, "TMPDIR", tmp)
	env = dropEnv(env, "TMP", "TEMP")

	deps := sandbox.LocalWrapDeps{
		Confine:    true,
		ExtraBinds: binds,
		NoExecBase: true,
		SelfExe:    func() (string, error) { return sd.Self, nil },
		EgressUnix: sd.EgressUnix,
		EgressAddr: sd.EgressAddr,
	}
	if sd.Self == "" {
		deps.SelfExe = nil
	}
	if sd.EgressUnix == nil {
		deps.EgressUnix = nil
	}
	if sd.EgressAddr == nil {
		deps.EgressAddr = nil
	}
	ports := listenPorts(s.Listen)
	profile := &sandbox.AgentProfile{ExecOnly: jailExecs(s)}
	if nw.deny {
		profile.PublicListenPorts = ports
	}
	deps.Agent = profile
	if nw.deny && spawnGOOS != "darwin" && len(s.Listen) > 0 {
		// Linux: the plugin listens inside its own namespace. The daemon
		// binds each configured address on the host and forwards every
		// connection in (sandbox Expose).
		exposes, stop, notes := forwardListens(s.Name, s.Listen)
		cleanups = append(cleanups, stop)
		deps.Exposes = exposes
		conf.Notes = append(conf.Notes, notes...)
	}

	w, wenv, c, werr := sandbox.WrapLocalCommand(spec, argv, "", env, deps)
	if werr != nil {
		return fail(werr)
	}
	cleanups = append(cleanups, c)
	return w, wenv, done, conf, nil
}

// jailFiles is the jail's filesystem allow-list beyond the base system, and
// the HOME and TMPDIR the plugin runs with.
func jailFiles(s Spec, sd SandboxDeps) (binds []sandbox.BindMount, home, tmp string, err error) {
	if s.BinPath != "" {
		// Its own binary's directory, read-only; it also holds the .path
		// directory of declared-command links.
		binds = append(binds, sandbox.BindMount{Path: filepath.Dir(s.BinPath), RO: true})
	}
	tmp = "/tmp" // the Linux jail's private tmpfs
	if sd.StagingRoot != "" {
		// Its own state: the staging subtree (one dir per instance) is
		// read-write, and HOME is a directory in it, so what the plugin
		// keeps (a CLI's cache, its config) persists and stays its own.
		stage := filepath.Join(sd.StagingRoot, s.Name)
		home = filepath.Join(stage, ".home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			return nil, "", "", fmt.Errorf("plugin %s: jail home: %w", s.Name, err)
		}
		binds = append(binds, sandbox.BindMount{Path: stage})
	}
	if spawnGOOS == "darwin" {
		d, err := os.MkdirTemp("", "conductor-plugin-")
		if err != nil {
			return nil, "", "", fmt.Errorf("plugin %s: jail tmp: %w", s.Name, err)
		}
		tmp = d
		binds = append(binds, sandbox.BindMount{Path: tmp})
	}
	if home == "" {
		home = tmp
	}
	add := func(entry string, defaultRW bool) {
		p, rw := config.ParsePluginFS(strings.TrimSpace(entry))
		p = config.ExpandHome(p)
		if !filepath.IsAbs(p) {
			return
		}
		binds = append(binds, sandbox.BindMount{Path: filepath.Clean(p), RO: !(rw || defaultRW), Optional: true})
	}
	// What the plugin declares it needs, then what the operator adds.
	for _, p := range s.EffectiveManifest().FS {
		add(p, false)
	}
	for _, p := range s.FS {
		add(p, false)
	}
	// Files the connection config points it at: read-only.
	for _, p := range s.Paths {
		binds = append(binds, sandbox.BindMount{Path: p, RO: true, Optional: true})
	}
	// Declared commands, at their resolved paths (read-only; executable,
	// which the jail's noexec program directories otherwise are not).
	for _, p := range commandPaths(s) {
		binds = append(binds, sandbox.BindMount{Path: p, RO: true, Optional: true})
	}
	return dedupeBinds(binds), home, tmp, nil
}

// commandPaths are the host paths of the plugin's declared commands: each
// one as PATH resolves it and, when that is a link, its target.
func commandPaths(s Spec) []string {
	var out []string
	for _, c := range s.EffectiveManifest().Commands {
		c = strings.TrimSpace(c)
		if c == "" || strings.ContainsAny(c, "/\\") {
			continue
		}
		lp, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		lp, _ = filepath.Abs(lp)
		real, err := filepath.EvalSymlinks(lp)
		if err != nil {
			continue
		}
		out = append(out, real)
		// The link itself, when it sits in a real directory (a package
		// manager's bin dir) rather than under a linked one (/bin → usr/bin,
		// which resolves inside the jail on its own).
		if real != lp {
			if d, err := filepath.EvalSymlinks(filepath.Dir(lp)); err == nil && d == filepath.Dir(lp) {
				out = append(out, lp)
			}
		}
	}
	return out
}

// jailExecs are the programs a jailed plugin may execute on macOS (Linux
// expresses the same with noexec program dirs and the command binds): its
// own binary and its declared commands.
func jailExecs(s Spec) []string {
	out := commandPaths(s)
	if s.BinPath != "" {
		out = append([]string{s.BinPath}, out...)
	}
	return out
}

// dedupeBinds drops repeated paths, keeping the most permissive entry (a
// read-write bind wins over a read-only one of the same path).
func dedupeBinds(in []sandbox.BindMount) []sandbox.BindMount {
	idx := map[string]int{}
	var out []sandbox.BindMount
	for _, b := range in {
		if i, ok := idx[b.Path]; ok {
			if !b.RO {
				out[i].RO = false
			}
			out[i].Optional = out[i].Optional && b.Optional
			continue
		}
		idx[b.Path] = len(out)
		out = append(out, b)
	}
	return out
}

// listenPorts are the ports of the listen addresses (0 and unparsable
// addresses skipped), sorted.
func listenPorts(addrs []string) []int {
	var out []int
	for _, a := range addrs {
		if _, p, err := net.SplitHostPort(a); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				out = append(out, n)
			}
		}
	}
	sort.Ints(out)
	return out
}

// forwardListens binds each listen address on the host and forwards every
// connection to the same port on the jail's own loopback, through a unix
// socket the in-jail forwarder serves (sandbox.Expose). An address that
// cannot be forwarded (port 0, already in use) is reported in notes, not
// fatal: the plugin still runs, that listener just is not reachable.
func forwardListens(name string, addrs []string) (exposes []sandbox.Expose, stop func(), notes []string) {
	dir, err := os.MkdirTemp("", "cpl-")
	if err != nil {
		return nil, func() {}, []string{fmt.Sprintf("cannot forward its listen addresses: %v", err)}
	}
	var lns []net.Listener
	for _, addr := range addrs {
		_, port, err := net.SplitHostPort(addr)
		if n, perr := strconv.Atoi(port); err != nil || perr != nil || n <= 0 {
			notes = append(notes, fmt.Sprintf("listen %s: not forwarded into its jail (needs a fixed port)", addr))
			continue
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			notes = append(notes, fmt.Sprintf("listen %s: not forwarded into its jail: %v", addr, err))
			continue
		}
		sock := filepath.Join(dir, port+".sock")
		lns = append(lns, ln)
		exposes = append(exposes, sandbox.Expose{Unix: sock, Dial: "127.0.0.1:" + port})
		go acceptForward(ln, sock)
		notes = append(notes, fmt.Sprintf("listen %s: forwarded into its jail", addr))
	}
	var once sync.Once
	stop = func() {
		once.Do(func() {
			for _, ln := range lns {
				_ = ln.Close()
			}
			_ = os.RemoveAll(dir)
		})
	}
	return exposes, stop, notes
}

// forwardDialWait bounds how long a forwarded connection waits for the
// in-jail forwarder's socket (a connection that arrives while the plugin is
// still starting).
var forwardDialWait = 5 * time.Second

func acceptForward(ln net.Listener, sock string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var u net.Conn
			deadline := time.Now().Add(forwardDialWait)
			for {
				u, err = net.Dial("unix", sock)
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err != nil {
				return
			}
			defer u.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(u, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
			<-done
		}()
	}
}

// dropEnv removes keys from a KEY=VALUE list.
func dropEnv(env []string, keys ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range keys {
			drop = drop || k == d
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
