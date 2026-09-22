package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CheckGOOS / CheckGeteuid / CheckLookPath feed Spec.Check's platform probe
// from inside WrapLocalCommand. They are package vars — not LocalWrapDeps
// fields — because they describe the ambient platform WrapLocalCommand
// itself runs on, not per-launch daemon wiring; tests override them to
// exercise the isolation paths without the wrapper binaries (sudo/unshare/
// docker) actually installed.
var (
	CheckGOOS     = runtime.GOOS
	CheckGeteuid  = os.Geteuid
	CheckLookPath = exec.LookPath
)

// LocalWrapDeps carries the daemon-side sandbox wiring WrapLocalCommand
// reuses to realize a spec's isolation around a local launch. Every field is
// optional; a nil minter with a spec that actually needs it is a launch
// error (fail closed), never a silent skip.
type LocalWrapDeps struct {
	// SelfExe resolves conductor's own executable — the binary WrapLocal
	// re-enters as the in-sandbox forwarder (`conductor sandbox-net`) when
	// masking or enforced egress is in play.
	SelfExe func() (string, error)
	// MaskPaths are the daemon's own sensitive paths hidden by default inside
	// a namespace-mode sandbox (state dir, config dir, …); `privileged: true`
	// on the isolation block opts out. Legacy deny-list path — superseded by
	// the pivot_root jail (Confine) which hides everything by absence.
	MaskPaths []string
	// Confine requests the pivot_root filesystem JAIL for a namespace-mode
	// launch (vs. the legacy deny-list masking): the sandbox sees ONLY the
	// workdir, the interpreter essentials, the caller's ExtraBinds, and the
	// spec's declared fs: paths — the daemon's state/config are hidden by
	// absence. Code steps set this; runtime launches leave it false (mask path).
	Confine bool
	// ExtraBinds are additional allow-list entries the caller needs inside the
	// jail beyond the workdir + spec.FS — e.g. a code step's own code temp dir
	// (read-only) and ctx-socket dir (read-write). Ignored unless Confine.
	ExtraBinds []BindMount
	// EgressAddr mints a LOOPBACK egress-proxy endpoint for an allowlist —
	// the ADVISORY path (HTTP(S)_PROXY env only).
	EgressAddr func(allow []string) (addr, cred string, revoke func(), err error)
	// EgressUnix mints the UNIX-socket egress-proxy endpoint for an
	// allowlist — the ENFORCED path (deny+allowlist under namespace/
	// container: the sandbox has no network of its own).
	EgressUnix func(allow []string) (sock, cred string, revoke func(), err error)
}

// WrapLocalCommand realizes spec's isolation around a local launch: the
// egress proxy (advisory loopback, or the OS-enforced unix-forwarded path
// under deny+namespace/container), the namespace mount-mask forwarder,
// spec.Check's platform preflight, and finally spec.WrapLocal itself. It is
// controller.prepareLaunch's LOCAL branch, generalized so any local-process
// launch — an agent runtime, or a code step's own subprocess
// (internal/code) — applies the SAME wrap around it.
//
// nil spec is a no-op: argv and env pass through completely unchanged,
// cleanup is a no-op, err is nil. A launch with no isolation: configured
// must behave exactly as if this function were never called.
//
// Fail-closed: any Check/proxy/wrap error aborts the whole launch (nil
// wrappedArgv/outEnv, non-nil err) rather than falling back to a bare,
// unconfined command.
func WrapLocalCommand(spec *Spec, argv []string, dir string, env []string, deps LocalWrapDeps) (wrappedArgv []string, outEnv []string, cleanup func(), err error) {
	noop := func() {}
	if spec == nil {
		return argv, env, noop, nil
	}
	cleanup = noop
	outEnv = append([]string(nil), env...)

	allow, useProxy := spec.ProxyPolicy()
	if useProxy {
		if deps.EgressAddr == nil {
			return nil, nil, noop, fmt.Errorf("sandbox: launch needs an egress proxy (isolation network policy) but none is wired")
		}
		addr, cred, revoke, perr := deps.EgressAddr(allow)
		if perr != nil {
			return nil, nil, noop, fmt.Errorf("sandbox: egress proxy: %w", perr)
		}
		if revoke != nil {
			cleanup = revoke
		}
		outEnv = append(outEnv, ProxyEnv(addr, cred)...)
	}

	var nf *NetForward
	jail := deps.Confine && spec.Mode == "namespace" && !spec.Privileged
	needMasks := !jail && spec.Mode == "namespace" && !spec.Privileged && len(deps.MaskPaths) > 0
	if jail {
		self, serr := resolveSelfExe(deps)
		if serr != nil {
			return nil, nil, cleanup, fmt.Errorf("sandbox: resolve conductor binary for sandbox jail: %w", serr)
		}
		nf = &NetForward{Self: self, Binds: jailBinds(dir, spec.FS, deps.ExtraBinds)}
	} else if needMasks {
		self, serr := resolveSelfExe(deps)
		if serr != nil {
			return nil, nil, cleanup, fmt.Errorf("sandbox: resolve conductor binary for sandbox masking: %w", serr)
		}
		nf = &NetForward{Self: self, Masks: append([]string(nil), deps.MaskPaths...)}
	}
	if spec.EnforcedEgress() {
		// The STRUCTURAL allowlist (#36 iso-review C1): the sandbox has no
		// network; its only path out is the forwarder into conductor's proxy
		// over a unix socket. Fails closed when unwired.
		if deps.EgressUnix == nil {
			return nil, nil, cleanup, fmt.Errorf("sandbox: enforced egress (deny+allowlist) needs the proxy's unix endpoint but none is wired")
		}
		sock, cred, revoke, perr := deps.EgressUnix(spec.Egress)
		if perr != nil {
			return nil, nil, cleanup, fmt.Errorf("sandbox: egress proxy socket: %w", perr)
		}
		if revoke != nil {
			cleanup = revoke
		}
		if nf == nil {
			self, serr := resolveSelfExe(deps)
			if serr != nil {
				return nil, nil, cleanup, fmt.Errorf("sandbox: resolve conductor binary for sandbox-net: %w", serr)
			}
			nf = &NetForward{Self: self}
		}
		nf.UnixSocket = sock
		if jail {
			// The forwarder dials this unix socket AFTER pivot_root, so its dir
			// must be inside the jail (bound read-write at its own path).
			nf.Binds = append(nf.Binds, BindMount{Path: filepath.Dir(sock)})
		}
		outEnv = append(outEnv, ProxyEnv(ForwardAddr, cred)...)
	}
	if err := spec.Check(CheckGOOS, CheckGeteuid(), CheckLookPath); err != nil {
		return nil, nil, cleanup, err
	}
	wrapped, werr := spec.WrapLocal(argv, dir, wrapEnvKeys(outEnv), nf)
	if werr != nil {
		return nil, nil, cleanup, werr
	}
	return wrapped, outEnv, cleanup, nil
}

// jailBinds assembles the pivot_root allow-list: the workdir (read-write), the
// step's declared fs: paths (read-write), and the caller's ExtraBinds (e.g. a
// code step's code temp dir read-only, ctx-socket dir read-write). buildJail
// adds the base system + /proc + /dev + /tmp on top; everything else is hidden
// by absence.
func jailBinds(dir string, fs []string, extra []BindMount) []BindMount {
	var b []BindMount
	if dir != "" {
		b = append(b, BindMount{Path: dir})
	}
	for _, p := range fs {
		if p != "" {
			b = append(b, BindMount{Path: p})
		}
	}
	return append(b, extra...)
}

// resolveSelfExe resolves conductor's own binary through deps.SelfExe,
// reporting clearly when the seam itself is unwired rather than panicking on
// a nil func value.
func resolveSelfExe(deps LocalWrapDeps) (string, error) {
	if deps.SelfExe == nil {
		return "", fmt.Errorf("no self-exe resolver wired")
	}
	return deps.SelfExe()
}

// wrapEnvKeys extracts the KEY halves of KEY=VALUE pairs (for the container
// mode's `-e KEY` pass-through).
func wrapEnvKeys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k != "" {
			out = append(out, k)
		}
	}
	return out
}
