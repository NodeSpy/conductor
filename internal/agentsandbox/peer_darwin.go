//go:build darwin

package agentsandbox

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID is the uid of the process on the other end of a unix socket
// (LOCAL_PEERCRED); ok=false when it cannot be read.
func PeerUID(conn net.Conn) (uint32, bool) {
	uid, _, ok := PeerCred(conn)
	return uid, ok
}

// PeerCred is PeerUID plus the peer's pid (LOCAL_PEERPID — macOS has no
// single getsockopt carrying both, unlike Linux's SO_PEERCRED) — used to
// anchor a skill session to the process on the other end of a control
// connection (e.g. `conductor sandbox exec`'s client), not merely check its
// uid (rt2 #12). ok=false when the uid cannot be read; pid is 0 when the uid
// was read but the pid lookup failed (uid-only binding still works).
func PeerCred(conn net.Conn) (uid uint32, pid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var cred *unix.Xucred
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); cerr != nil || serr != nil || cred == nil {
		return 0, 0, false
	}
	var p int
	_ = raw.Control(func(fd uintptr) {
		if v, perr := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); perr == nil {
			p = v
		}
	})
	return cred.Uid, p, true
}
