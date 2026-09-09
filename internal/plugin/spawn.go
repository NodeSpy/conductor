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
// never in env, never in argv. Mirrors internal/code/gorun.go: spawnBaseEnv.
func spawnBaseEnv() []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"SHELL": true, "TERM": true, "TZ": true, "LANG": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if allow[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// EgressUnixFunc mints an OS-enforced egress proxy endpoint for an allowlist,
// returning the daemon-side unix socket, a per-launch credential, and a revoke
// closure. Wired from main (sandbox.ProxyManager.UnixEndpoint); nil in contexts
// with no proxy manager (tests), where enforced egress is unavailable.
type EgressUnixFunc func(allow []string) (sock, cred string, revoke func(), err error)

// SandboxDeps carries the daemon-side sandbox wiring a plugin launch reuses
// (the #36 §15 layer). All fields are optional: with none set, a plugin with an
// isolation: block still gets process/mount/pid isolation and structural
// no-network (network.deny), just not the allowlist egress proxy.
type SandboxDeps struct {
	Self       string         // conductor's own executable (os.Executable()) — the in-sandbox forwarder
	MaskPaths  []string       // daemon paths hidden inside the plugin's mount namespace
	EgressUnix EgressUnixFunc // enforced-egress endpoint minter
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
		// No isolation block: run directly with a scrubbed env. The operator
		// is told (loudly, by the manager) that OS confinement is off.
		c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go) and operator-configured
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
