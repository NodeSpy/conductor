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

// NetForward carries the enforced-egress wiring into WrapLocal: the path of
// this conductor binary (re-executed inside the sandbox as the forwarder)
// and the egress proxy's unix socket.
type NetForward struct {
	Self       string   // conductor's own executable path
	UnixSocket string   // the proxy's unix socket (daemon side); "" = no net forward
	Masks      []string // daemon paths to hide inside the mount namespace (#36 iso-review H7)
}

// EnterOpts is the `conductor sandbox-net` helper's configuration.
type EnterOpts struct {
	Listen string   // TCP address to serve inside the sandbox ("" = none)
	Unix   string   // the egress proxy's unix socket path
	Masks  []string // paths to overmount away before exec (#36 iso-review H7)
	Argv   []string // the real launch to exec once the plumbing is up
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
	// Masking first: the daemon's own files disappear from this mount
	// namespace before anything else runs. Fail closed — a mask that cannot
	// be applied must not silently leave the files readable.
	for _, m := range opt.Masks {
		if err := maskPath(m); err != nil {
			fmt.Fprintf(os.Stderr, "sandbox-net: mask %s: %v\n", m, err)
			return 1
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
	// Privilege drop before exec (#36 iso-review round 2, item 1): the masks
	// above are overmounts in THIS mount namespace, whose owning user namespace
	// (the outer `unshare --user`) still grants the process CAP_SYS_ADMIN — so a
	// plain exec would hand the untrusted payload the power to `umount` a mask
	// and read the daemon file underneath. When masks are in force, re-exec the
	// payload through a SECOND, nested user namespace it does NOT own the mount
	// namespace from: it holds caps only over that new namespace, none over the
	// mount ns where the masks live, so umount/remount of a mask is refused by
	// the kernel (and mounts inherited into the less-privileged ns are locked,
	// closing the make-a-new-mount-ns-and-umount-there bypass). Fail closed —
	// if the hop can't be set up the payload does not run unprotected.
	cmd := execChild(opt.Argv, len(opt.Masks) > 0)
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
