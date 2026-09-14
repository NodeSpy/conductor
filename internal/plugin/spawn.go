package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

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
// no-network (network.deny), just not the allowlist egress proxy.
type SandboxDeps struct {
	Self       string         // conductor's own executable (os.Executable()) — the in-sandbox forwarder
	MaskPaths  []string       // daemon paths hidden inside the plugin's mount namespace
	EgressUnix EgressUnixFunc // enforced-egress endpoint minter (namespaced launch)
	EgressAddr EgressAddrFunc // enforced-egress endpoint minter (default launch)
}

// buildCommand prepares the (possibly sandbox-wrapped) *exec.Cmd for a plugin,
// plus a cleanup closure to run when the process exits (revokes any egress
// credential). It does NOT start the process. Returns sandboxed=false when the
// launch runs without OS confinement (no isolation: block) so the caller can
// warn — env scrubbing and all transport guards still apply.
func buildCommand(s Spec, sd SandboxDeps) (cmd *exec.Cmd, cleanup func(), sandboxed bool, err error) {
	argv := append([]string{s.BinPath}, s.Args...)
	env := spawnBaseEnv()
	cleanup = func() {}

	spec := sandbox.FromConfig(s.Isolation)
	if spec == nil {
		// NO isolation block is the NORMAL case under the app-extension model:
		// conductor is a privileged app the operator chose to run, and a plugin
		// they added is one too. The default confinement is the permission
		// manifest — the plugin's declared commands and egress, surfaced when it
		// is added and enforced here — not an OS jail. `isolation:` on the
		// referencing entry is opt-in hardening for a locked-down box.
		//
		// Env scrubbing, per-call credential delivery, verify-before-execute,
		// size-bounded responses, and supervision all still apply.
		env, cleanup, err = confineToManifest(s, env, sd)
		if err != nil {
			return nil, nil, false, err
		}
		c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go); confinement is the declared manifest
		c.Env = env
		return c, cleanup, false, nil
	}

	// Fail-closed preflight: wrapper binaries present, namespace only on Linux
	// non-root, etc. A misconfigured sandbox refuses to launch — never silently
	// downgrades to no isolation.
	if err := spec.Check(runtime.GOOS, os.Geteuid(), exec.LookPath); err != nil {
		return nil, nil, false, fmt.Errorf("plugin %s: sandbox preflight: %w", s.Name, err)
	}

	var nf *sandbox.NetForward
	if len(sd.MaskPaths) > 0 || spec.EnforcedEgress() {
		nf = &sandbox.NetForward{Self: sd.Self, Masks: sd.MaskPaths}
	}
	if spec.EnforcedEgress() {
		if sd.EgressUnix == nil {
			return nil, nil, false, fmt.Errorf("plugin %s: isolation requests an egress allowlist but no egress proxy is wired", s.Name)
		}
		sock, cred, revoke, err := sd.EgressUnix(spec.Egress)
		if err != nil {
			return nil, nil, false, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
		}
		nf.UnixSocket = sock
		env = append(env, sandbox.ProxyEnv(sandbox.ForwardAddr, cred)...)
		cleanup = revoke
	}

	wrapped, err := spec.WrapLocal(argv, "", envKeys(env), nf)
	if err != nil {
		cleanup()
		return nil, nil, false, fmt.Errorf("plugin %s: sandbox wrap: %w", s.Name, err)
	}
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, sandbox-wrapped
	c.Env = env
	return c, cleanup, true, nil
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

	if len(m.Egress) > 0 && sd.EgressAddr != nil {
		addr, cred, revoke, err := sd.EgressAddr(m.Egress)
		if err != nil {
			return nil, nil, fmt.Errorf("plugin %s: egress proxy for declared network (%s): %w", s.Name, strings.Join(m.Egress, ", "), err)
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
