package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// spawnGOOS / spawnLookPath are the platform probes buildCommand uses to pick
// the sandbox backend and preflight the wrapper binaries — package vars so a
// test can exercise the macOS (Seatbelt) branch from a Linux host, the same way
// sandbox.CheckGOOS is injectable.
var (
	spawnGOOS     = runtime.GOOS
	spawnLookPath = exec.LookPath
)

// masksExcludingBinaryDir drops any mask path that is the plugin binary's
// directory or an ancestor of it. Masking such a path (a tmpfs overmount) is
// refused by the kernel when the directory subtree holds a running executable,
// and would only hide the plugin's own binary from itself. binPath == "" (a
// not-installed spec) leaves the list unchanged.
func masksExcludingBinaryDir(masks []string, binPath string) []string {
	if binPath == "" || len(masks) == 0 {
		return masks
	}
	binDir := filepath.Dir(binPath)
	kept := make([]string, 0, len(masks))
	for _, m := range masks {
		if m == binDir || strings.HasPrefix(binDir+string(filepath.Separator), m+string(filepath.Separator)) {
			continue // m == binDir, or m is an ancestor of binDir
		}
		kept = append(kept, m)
	}
	return kept
}

// spawnBaseEnv is the minimal environment a plugin subprocess inherits:
// operational basics only. The daemon's full environment routinely carries
// credentials (webhook secrets, tokens), so forwarding os.Environ() wholesale
// would hand every one of them to third-party code (§8.1). A plugin's
// instance credential is delivered per-call over the RPC transport instead —
// never in env, never in argv. Shared with the env-scrubbed ACP runtime path
// via sandbox.MinimalEnv.
func spawnBaseEnv() []string {
	return sandbox.MinimalEnv()
}

// grantedEnv is what a plugin may read from the daemon's environment: the
// variables it declares (Capabilities.Env) AND the operator granted
// (allow_env). A declaration alone grants nothing — the daemon's environment
// carries the operator's secrets under names of their choosing.
func grantedEnv(s Spec) []string {
	declared := map[string]bool{}
	for _, n := range s.EffectiveManifest().Env {
		declared[n] = true
	}
	var out []string
	for _, n := range s.AllowEnv {
		if declared[n] {
			out = append(out, n)
		}
	}
	return out
}

// withDeclaredEnv passes the daemon's values of the named variables through
// to a plugin. Conductor's own control variables are never passed.
func withDeclaredEnv(env, names []string) []string {
	for _, n := range names {
		if n == "" || strings.HasPrefix(n, "CONDUCTOR_") || strings.ContainsAny(n, "= ") {
			continue
		}
		if v, ok := os.LookupEnv(n); ok {
			env = replaceEnv(env, n, v)
		}
	}
	return env
}

// EgressUnixFunc mints an OS-enforced egress proxy endpoint for an allowlist,
// returning the daemon-side unix socket, a per-launch credential, and a revoke
// closure. Wired from main (sandbox.ProxyManager.UnixEndpoint); nil in contexts
// with no proxy manager (tests), where enforced egress is unavailable.
//
// The unix form is for a launch inside a MOUNT NAMESPACE, where an in-sandbox
// forwarder bridges a fixed loopback address to this socket. The default
// (manifest-confined, un-namespaced) launch has no forwarder and needs a plain
// loopback address instead — that is EgressAddrFunc.
type EgressUnixFunc func(allow []string) (sock, cred string, revoke func(), err error)

// EgressAddrFunc mints a LOOPBACK egress proxy endpoint for an allowlist. It is
// what confines a plugin to its declared network on the default path, where
// there is no namespace and therefore no forwarder. Wired from main
// (sandbox.ProxyManager.Endpoint); nil leaves the declared network unenforced
// (and the caller says so rather than pretending).
type EgressAddrFunc func(allow []string) (addr, cred string, revoke func(), err error)

// SandboxDeps carries the daemon-side sandbox wiring a plugin launch reuses
// (the #36 §15 layer). All fields are optional: with none set, a plugin with an
// isolation: block still gets process/mount/pid isolation and structural
// no-network (network.deny), just not the allowlist egress proxy, and no
// plugin gets the default jail (Jail is JailOff).
type SandboxDeps struct {
	Self      string   // conductor's own executable (os.Executable()) — the in-sandbox forwarder
	MaskPaths []string // daemon paths hidden inside an isolation: block's mount namespace
	// StagingRoot holds each plugin's staging directories (StagingDir); the
	// plugin's own subtree is writable inside its sandbox. "" gives none.
	StagingRoot string
	EgressUnix  EgressUnixFunc // enforced-egress endpoint minter (namespaced launch)
	EgressAddr  EgressAddrFunc // enforced-egress endpoint minter (default launch)
	// Jail is the default jail policy (sandbox.plugins) for a plugin with no
	// isolation: block and no trust: full. See jail.go.
	Jail JailPolicy
	// JailProbe reports why this box cannot build the default jail ("" when
	// it can). nil: assume it can.
	JailProbe func() string
}

// buildCommand prepares the (possibly sandbox-wrapped) *exec.Cmd for a plugin,
// plus a cleanup closure to run when the process exits (revokes any egress
// credential, closes listen forwarders). It does NOT start the process. conf
// says how the process is confined, for the spawn log.
//
// Which confinement a plugin gets, in order:
//
//   - trust: full, or an isolation: block with mode none: no OS sandbox; the
//     declared manifest (egress via the proxy variables, a PATH of declared
//     commands) applies, as it did before the jail existed.
//   - an isolation: block: exactly that sandbox, fail-closed.
//   - sandbox.plugins: off: as trust: full.
//   - otherwise the default jail (jail.go). Where this box cannot build a
//     jail at all (JailProbe), sandbox.plugins: on runs the plugin as
//     trust: full would, with a warning, and sandbox.plugins: required
//     refuses to start it. A jail that fails to build for this one launch
//     is refused under either.
//
// Env scrubbing, per-call credential delivery, verify-before-execute,
// size-bounded responses and supervision apply in every case.
func buildCommand(s Spec, sd SandboxDeps) (cmd *exec.Cmd, cleanup func(), conf Confinement, err error) {
	argv := append([]string{s.BinPath}, s.Args...)
	env := withDeclaredEnv(spawnBaseEnv(), grantedEnv(s))
	cleanup = func() {}

	manifestOnly := func(c Confinement) (*exec.Cmd, func(), Confinement, error) {
		env, cleanup, err := confineToManifest(s, env, sd)
		if err != nil {
			return nil, nil, c, err
		}
		cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go); confinement is the declared manifest
		cmd.Env = env
		return cmd, cleanup, c, nil
	}

	spec := sandbox.FromConfig(s.Isolation)
	switch {
	case s.TrustFull || s.Isolation.OptsOut():
		return manifestOnly(Confinement{Mode: "trusted"})
	case spec != nil:
		return isolationCommand(s, sd, spec, argv, env)
	case sd.Jail == JailOff && s.Kind != KindStep:
		// sandbox.plugins: off is the pre-jail posture, in which a code-step
		// engine was already jailed by default; it stays jailed.
		return manifestOnly(Confinement{Mode: "off"})
	}

	// A declared fs: beyond the cap is refused at every launch, not only at
	// install (an install record from before the cap, a local build), and
	// never falls back to running without the jail.
	if err := CheckDeclaredFS(s.Name, s.EffectiveManifest().FS, s.FS, ProtectedDirs(sd.MaskPaths...)); err != nil {
		return nil, nil, Confinement{}, err
	}

	why := ""
	if sd.JailProbe != nil {
		why = sd.JailProbe()
	}
	if why == "" {
		var jenv []string
		var wrapped []string
		var c Confinement
		// The confined PATH (declared commands only) is part of the jail too.
		penv := env
		if dir, perr := commandPathDir(s); perr != nil {
			return nil, nil, Confinement{}, fmt.Errorf("plugin %s: confining declared commands: %w", s.Name, perr)
		} else if dir != "" {
			penv = replaceEnv(penv, "PATH", dir)
		}
		wrapped, jenv, cleanup, c, err = buildJail(s, sd, argv, penv)
		if err == nil {
			cmd := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, jailed
			cmd.Env = jenv
			return cmd, cleanup, c, nil
		}
		// This box can build a jail; this launch's could not be built (its
		// staging dir is not usable, a proxy endpoint failed). That is
		// refused under on as well: the fallback is for a box without the
		// mechanism, not for a launch a plugin may have broken itself (a
		// file planted where its HOME dir goes).
		return nil, nil, Confinement{}, fmt.Errorf("plugin %s: building its OS jail failed, not starting it: %w", s.Name, err)
	}
	if sd.Jail == JailRequired {
		return nil, nil, Confinement{}, fmt.Errorf("plugin %s: its OS jail cannot be built on this box (%s), and sandbox.plugins is required — not starting it", s.Name, why)
	}
	return manifestOnly(Confinement{Mode: "unavailable", Why: why})
}

// isolationCommand is the launch under the operator's own isolation: block:
// that sandbox exactly, and any failure to build it refuses the launch.
func isolationCommand(s Spec, sd SandboxDeps, spec *sandbox.Spec, argv, env []string) (*exec.Cmd, func(), Confinement, error) {
	conf := Confinement{Mode: "isolation"}
	cleanup := func() {}
	stage := ""
	if sd.StagingRoot != "" {
		// The plugin's staging subtree must exist to be bound, and is the one
		// path under the daemon's state it may write.
		stage = filepath.Join(sd.StagingRoot, s.Name)
		if err := os.MkdirAll(stage, 0o700); err != nil {
			return nil, nil, conf, fmt.Errorf("plugin %s: staging dir: %w", s.Name, err)
		}
		spec.FS = append(spec.FS, stage)
	}
	if err := spec.Check(spawnGOOS, os.Geteuid(), spawnLookPath); err != nil {
		return nil, nil, conf, fmt.Errorf("plugin %s: sandbox preflight: %w", s.Name, err)
	}

	// An operator-written isolation block requests masking of the daemon's
	// state/config dirs (and fails closed if it can't be applied). A
	// plugin's own binary dir is never maskable (its running text lives
	// there).
	masks := masksExcludingBinaryDir(sd.MaskPaths, s.BinPath)

	var nf *sandbox.NetForward
	switch {
	case spawnGOOS == "darwin" && spec.Mode == "namespace":
		// macOS Seatbelt is an ALLOW-LIST (deny-default), the inverse of the
		// Linux Masks deny-list — which has no Seatbelt analog. So instead of
		// hiding the daemon's dirs, we allow-list only what the plugin needs:
		// its own binary (read-only; interpreter/system libs come from the
		// base profile) plus the network verdict from spec.Deny. The plugin
		// transport is stdio (inherited fds), so no socket needs binding.
		if s.BinPath != "" {
			nf = &sandbox.NetForward{Binds: []sandbox.BindMount{{Path: filepath.Dir(s.BinPath), RO: true}}}
		}
		if stage != "" {
			if nf == nil {
				nf = &sandbox.NetForward{}
			}
			nf.Binds = append(nf.Binds, sandbox.BindMount{Path: stage})
		}
	case len(masks) > 0 || spec.EnforcedEgress():
		nf = &sandbox.NetForward{Self: sd.Self, Masks: masks}
	}
	if spec.EnforcedEgress() {
		if sd.EgressUnix == nil {
			return nil, nil, conf, fmt.Errorf("plugin %s: isolation requests an egress allowlist but no egress proxy is wired", s.Name)
		}
		sock, cred, revoke, err := sd.EgressUnix(spec.Egress)
		if err != nil {
			return nil, nil, conf, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
		}
		nf.UnixSocket = sock
		env = append(env, sandbox.ProxyEnv(sandbox.ForwardAddr, cred)...)
		cleanup = revoke
	}

	wrapped, err := spec.WrapLocal(argv, "", envKeys(env), nf)
	if err != nil {
		cleanup()
		return nil, nil, conf, fmt.Errorf("plugin %s: sandbox wrap: %w", s.Name, err)
	}
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, sandbox-wrapped
	c.Env = env
	return c, cleanup, conf, nil
}

// confineToManifest applies the DEFAULT (non-isolation) confinement: the
// plugin's effective permission manifest.
//
//   - Egress: when the plugin declared hosts (narrowed by the connector's
//     `network:`), the child is pointed at conductor's egress proxy with an
//     allowlist of exactly that set. This is the same enforced path the
//     isolation: block uses — the confinement is real.
//   - Commands: PATH is replaced with a directory holding links to exactly the
//     declared commands, so an undeclared tool is not resolvable by name. See
//     manifest.go for what this does and does not stop.
//
// A plugin that declares nothing is confined to nothing beyond the scrubbed
// env: it declared no needs, so there is no allowlist to build, and inventing
// one would break plugins that predate the manifest.
func confineToManifest(s Spec, env []string, sd SandboxDeps) ([]string, func(), error) {
	cleanup := func() {}
	m := s.EffectiveManifest()

	// A STEP ENGINE that declares no egress gets an EMPTY allowlist rather
	// than no allowlist: deny-by-default, because an engine's job is to
	// execute the operator's own code against conductor's data plane, and
	// "reach the internet as well" is a thing it should have to say out loud.
	//
	// Connectors keep the opposite default, and must: a connector that
	// predates the manifest declares nothing and calls the service it exists
	// to call, so an empty allowlist there would break plugins in the field.
	// Engines have no field to break — this is their first release.
	//
	// Honest about what this is: the proxy is delivered as HTTP(S)_PROXY, so
	// it confines a cooperating client, which is the same manifest-level
	// confinement every non-isolation: plugin gets (see the package doc). An
	// `isolation:` block is what turns it into an OS-enforced wall.
	//
	// A Probe Spec (finding 2) ALSO confines even though its allowlist is
	// empty — the opposite of a connector's normal "empty allow means
	// unconfined" default (the rule right above this comment). It is never a
	// real connector that merely declared no egress; it is the type-level
	// `plugin.describe` probe, which must get NO network at all. Routed
	// through the same enforced proxy path as a step engine's deny-by-default
	// case, with an allowlist of nothing: every host is refused. Where
	// sd.EgressAddr is nil (no proxy manager wired — this default,
	// non-isolation: path has no OTHER enforcement mechanism) the probe runs
	// with no confinement at all, same as every other plugin on this path
	// without a wired proxy; realDial's confinement log line says what it
	// runs under.
	allow := m.Egress
	confine := len(allow) > 0 || s.Kind == KindStep || s.Probe
	if confine && sd.EgressAddr != nil {
		addr, cred, revoke, err := sd.EgressAddr(allow)
		if err != nil {
			return nil, nil, fmt.Errorf("plugin %s: egress proxy for declared network (%s): %w", s.Name, strings.Join(allow, ", "), err)
		}
		env = append(env, sandbox.ProxyEnv(addr, cred)...)
		cleanup = revoke
	}

	if dir, err := commandPathDir(s); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("plugin %s: confining declared commands: %w", s.Name, err)
	} else if dir != "" {
		env = replaceEnv(env, "PATH", dir)
	}
	return env, cleanup, nil
}

// replaceEnv sets key=val in a KEY=VALUE list, replacing any existing entry.
func replaceEnv(env []string, key, val string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+val)
}

// envKeys returns just the KEY parts of KEY=VALUE env lines (container mode
// pass-through needs the names).
func envKeys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			out = append(out, k)
		}
	}
	return out
}
