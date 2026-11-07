package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/scratch"
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
// no-network (network.deny), just not the allowlist egress proxy.
type SandboxDeps struct {
	Self      string   // conductor's own executable (os.Executable()) — the in-sandbox forwarder
	MaskPaths []string // daemon paths hidden inside the plugin's mount namespace
	// StagingRoot holds each plugin's staging directories (StagingDir); the
	// plugin's own subtree is writable inside its sandbox. "" gives none.
	StagingRoot string
	EgressUnix  EgressUnixFunc // enforced-egress endpoint minter (namespaced launch)
	EgressAddr  EgressAddrFunc // enforced-egress endpoint minter (default launch)
}

// buildCommand prepares the (possibly sandbox-wrapped) *exec.Cmd for a plugin,
// plus a cleanup closure to run when the process exits (revokes any egress
// credential). It does NOT start the process. Returns sandboxed=false when the
// launch runs without OS confinement (no isolation: block) so the caller can
// warn — env scrubbing and all transport guards still apply.
func buildCommand(s Spec, sd SandboxDeps) (cmd *exec.Cmd, cleanup func(), sandboxed bool, err error) {
	// The plugin's TMPDIR is a directory of its own in its staging subtree
	// (on disk in the scratch dir, writable in its sandbox), removed when the
	// process exits: what a plugin's tools (git, a compiler) put in TMPDIR
	// never lands in a RAM-backed /tmp.
	var tmp *scratch.Held
	if sd.StagingRoot != "" {
		if tmp, err = scratch.NewHeld(filepath.Join(sd.StagingRoot, s.Name), scratch.PluginTmpPrefix); err != nil {
			return nil, nil, false, fmt.Errorf("plugin %s: temp dir: %w", s.Name, err)
		}
	}
	cmd, cleanup, sandboxed, err = buildCommandIn(s, sd)
	if tmp == nil {
		return cmd, cleanup, sandboxed, err
	}
	if err != nil {
		tmp.Remove()
		return nil, nil, false, err
	}
	cmd.Env = replaceEnv(cmd.Env, "TMPDIR", tmp.Path)
	inner := cleanup
	cleanup = func() { inner(); tmp.Remove() }
	return cmd, cleanup, sandboxed, nil
}

func buildCommandIn(s Spec, sd SandboxDeps) (cmd *exec.Cmd, cleanup func(), sandboxed bool, err error) {
	argv := append([]string{s.BinPath}, s.Args...)
	if s.TrustFull {
		// The operator opted this plugin out of its jail: it runs as
		// conductor, with only its environment scrubbed (the variables it
		// declares pass, narrowed by allow_env).
		g, gerr := pluginGrant(s)
		if gerr != nil {
			return nil, nil, false, gerr
		}
		c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go); trust: full
		c.Env = withDeclaredEnv(spawnBaseEnv(), g.Env)
		return c, func() {}, false, nil
	}
	if h, before := runsAsBefore(s); before {
		return buildAsBefore(s, sd, argv)
	} else if !h.OK {
		return nil, nil, false, h.Refusal(s.Name)
	}
	// An isolation: block is replaced by the jail (the config warns).
	c, cleanup, err := buildJailed(s, sd, argv, spawnBaseEnv())
	if err != nil {
		return nil, nil, false, err
	}
	return c, cleanup, true, nil
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
