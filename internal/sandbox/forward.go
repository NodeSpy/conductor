// The in-sandbox side of ENFORCED egress (#36 iso-review C1). With
// `network: {deny: true, egress: [...]}` under namespace/container mode, the
// sandboxed process has NO network interface (unshare --net /
// --network=none) — except a TCP forwarder this file implements, launched
// inside the sandbox as `conductor sandbox-net`, that pipes every accepted
// connection into conductor's egress proxy over a UNIX socket. Unix sockets
// are filesystem objects, not network endpoints, so they cross the network
// namespace boundary while the (empty) namespace blocks everything else —
// DNS included: the proxy resolves CONNECT targets itself, outside the
// sandbox, so name-resolution exfiltration paths are closed with the rest.
//
// The runtime inside sees HTTP(S)_PROXY=http://<cred>@127.0.0.1:18080 —
// in-namespace loopback, reachable from inside and nowhere else — and the
// allowlist is enforced by the proxy on the daemon side of the socket. The
// egress: list under deny is a structural boundary, not an advisory one.
package sandbox

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
)

// ForwardAddr is the fixed in-sandbox address the forwarder listens on and
// the launch's HTTP(S)_PROXY points at. The namespace/container network is
// empty, so the port is always free there.
const ForwardAddr = "127.0.0.1:18080"

// BindMount is one entry of a namespace-jail ALLOW-LIST: a host path made
// visible inside the pivot_root'd sandbox at the same path. RO entries are
// remounted read-only. The jail contains ONLY these paths (plus the base
// system + /proc + /dev + /tmp buildJail sets up) — the daemon's own state and
// config are hidden by ABSENCE, never bound in, which is why the jail needs no
// mask overmounts (and dodges the EPERM those hit under an unprivileged
// user namespace).
type BindMount struct {
	Path string // host path, mounted at the same path inside the jail
	RO   bool   // remount read-only after binding
}

// NetForward carries the in-sandbox wiring into WrapLocal: the path of this
// conductor binary (re-executed inside the sandbox as `conductor sandbox-net`),
// the egress proxy's unix socket (enforced egress), and either a pivot_root
// allow-list (Binds — the fs jail) or the legacy deny-list Masks. Binds and
// Masks are mutually exclusive; when Binds is set the launch is jailed and the
// unshare maps the daemon uid to root-in-userns so the mounts are permitted.
type NetForward struct {
	Self       string      // conductor's own executable path
	UnixSocket string      // the proxy's unix socket (daemon side); "" = no net forward
	Masks      []string    // legacy: daemon paths to overmount away (plugin path; #36 iso-review H7)
	Binds      []BindMount // fs-jail allow-list to pivot_root into ("" = no jail)
}

// EnterOpts is the `conductor sandbox-net` helper's configuration.
type EnterOpts struct {
	Listen string      // TCP address to serve inside the sandbox ("" = none)
	Unix   string      // the egress proxy's unix socket path
	Masks  []string    // legacy: paths to overmount away before exec (#36 iso-review H7)
	Binds  []BindMount // fs-jail allow-list; non-empty ⇒ pivot_root jail instead of masks
	Argv   []string    // the real launch to exec once the plumbing is up
}

// RunEnter is the `conductor sandbox-net` entry point: bring the sandbox
// loopback up, start the TCP→unix forwarder, then run the wrapped launch,
// returning its exit code. It runs INSIDE the namespace/container as PID-1's
// child — when the launch exits, the process (and with it the namespace and
// the forwarder) is torn down.
func RunEnter(opt EnterOpts) int {
	if len(opt.Argv) == 0 {
		fmt.Fprintln(os.Stderr, "sandbox-net: nothing to run (missing -- argv)")
		return 2
	}
	// Filesystem confinement first — before any forwarder or exec. Fail closed:
	// a jail/mask that cannot be applied must NOT silently leave the host fs
	// (and the daemon's secrets) readable.
	//
	// Two mechanisms: the pivot_root ALLOW-LIST jail (Binds — the strong path,
	// hides everything not declared, used for code steps) OR the legacy
	// deny-list Masks (overmount specific daemon dirs, used by the plugin path).
	// They are mutually exclusive; Binds wins when both are somehow present.
	jailed := len(opt.Binds) > 0
	if jailed {
		if err := buildJail(opt.Binds); err != nil {
			fmt.Fprintf(os.Stderr, "sandbox-net: jail: %v\n", err)
			return 1
		}
	} else {
		for _, m := range opt.Masks {
			if err := maskPath(m); err != nil {
				fmt.Fprintf(os.Stderr, "sandbox-net: mask %s: %v\n", m, err)
				return 1
			}
		}
	}
	if opt.Listen != "" {
		if opt.Unix == "" {
			fmt.Fprintln(os.Stderr, "sandbox-net: --listen needs --unix (the egress proxy socket)")
			return 2
		}
		// A fresh netns starts with lo DOWN; the userns owns it, so we can
		// bring it up unprivileged. In a container lo is already up — no-op.
		if err := loopbackUp(); err != nil {
			fmt.Fprintf(os.Stderr, "sandbox-net: loopback up: %v\n", err)
			return 1
		}
		ln, err := net.Listen("tcp", opt.Listen)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sandbox-net: listen %s: %v\n", opt.Listen, err)
			return 1
		}
		defer ln.Close()
		go serveForward(ln, opt.Unix)
	}
	// Privilege drop before exec (#36 iso-review round 2, item 1): the jail /
	// masks above were applied in THIS mount namespace, whose owning user
	// namespace still grants the process CAP_SYS_ADMIN — so a plain exec would
	// hand the untrusted payload the power to remount a read-only bind, umount a
	// mask, or pivot_root back out of the jail. When any fs confinement is in
	// force, re-exec the payload through a SECOND, nested user namespace it does
	// NOT own the mount namespace from: it holds caps only over that new
	// namespace, none over the mount ns the jail/masks live in, so umount /
	// remount / pivot is refused by the kernel (and mounts inherited into the
	// less-privileged ns are locked, closing the make-a-new-mount-ns bypass).
	// Fail closed — if the hop can't be set up the payload does not run
	// unprotected. (Proven end-to-end: a dropped child can read allow-listed
	// paths but cannot remount /usr rw or bind / — see the sandbox spikes.)
	cmd := execChild(opt.Argv, jailed || len(opt.Masks) > 0)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "sandbox-net: exec: %v\n", err)
		return 1
	}
	return 0
}

// serveForward pipes each accepted in-sandbox connection into the proxy's
// unix socket. Fail-closed by construction: if the socket is gone, the
// connection just drops — nothing falls back to a direct network (there is
// none to fall back to).
func serveForward(ln net.Listener, unixPath string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			u, err := net.Dial("unix", unixPath)
			if err != nil {
				c.Close()
				return
			}
			go func() {
				defer c.Close()
				defer u.Close()
				_, _ = io.Copy(u, c)
			}()
			defer c.Close()
			defer u.Close()
			_, _ = io.Copy(c, u)
		}(c)
	}
}
