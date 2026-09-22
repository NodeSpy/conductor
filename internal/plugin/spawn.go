package plugin

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
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

	// Preflight: wrapper binaries present, namespace only on Linux non-root, etc.
	// An OPERATOR-WRITTEN isolation block fails closed — a misconfigured sandbox
	// refuses to launch, never silently downgrades. A conductor-SYNTHESIZED
	// default (an untrusted-by-default code engine) is best-effort instead: where
	// the OS sandbox can't be applied it degrades to the manifest-only path with a
	// loud warning, so an engine that would run today keeps running rather than
	// the daemon refusing to start it.
	if err := spec.Check(runtime.GOOS, os.Geteuid(), exec.LookPath); err != nil {
		if s.IsolationDefaulted {
			log.Printf("plugin %s: default sandbox unavailable (%v) — running WITHOUT OS confinement; install util-linux (unshare) + enable unprivileged user namespaces to sandbox it", s.Name, err)
			env, cleanup, cerr := confineToManifest(s, spawnBaseEnv(), sd)
			if cerr != nil {
				return nil, nil, false, cerr
			}
			c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go); confinement is the declared manifest
			c.Env = env
			return c, cleanup, false, nil
		}
		return nil, nil, false, fmt.Errorf("plugin %s: sandbox preflight: %w", s.Name, err)
	}

	// Daemon-path masking (a tmpfs overmount of the state/config dirs) is
	// unreliable inside an unprivileged `unshare --user` namespace: the kernel
	// refuses to overmount a locked inherited mount (EPERM), which is why tools
	// like bwrap pivot_root instead. So the SYNTHESIZED default sandbox skips
	// masking entirely and relies on what a userns reliably gives — user + pid +
	// network(deny) namespaces — which already blocks the big risks (egress and
	// process tampering) for a pure-compute engine. An OPERATOR-WRITTEN isolation
	// block still requests masking (and fails closed if it can't be applied — the
	// operator chose it and should see it). A plugin's own binary dir is never
	// maskable regardless (its running text lives there).
	masks := masksExcludingBinaryDir(sd.MaskPaths, s.BinPath)
	if s.IsolationDefaulted {
		masks = nil
	}

	var nf *sandbox.NetForward
	if len(masks) > 0 || spec.EnforcedEgress() {
		nf = &sandbox.NetForward{Self: sd.Self, Masks: masks}
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
	allow := m.Egress
	confine := len(allow) > 0 || s.Kind == KindStep
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
