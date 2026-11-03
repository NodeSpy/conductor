//go:build linux

package agentsandbox

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID is the uid of the process on the other end of a unix socket
// (SO_PEERCRED); ok=false when it cannot be read.
func PeerUID(conn net.Conn) (uint32, bool) {
	uid, _, ok := PeerCred(conn)
	return uid, ok
}

// PeerCred is PeerUID plus the peer's pid (SO_PEERCRED carries both) — used
// to anchor a skill session to the process on the other end of a control
// connection (e.g. `conductor sandbox exec`'s client), not merely check its
// uid (rt2 #12). ok=false when neither can be read; pid is 0 in that case.
func PeerCred(conn net.Conn) (uid uint32, pid int, ok bool) {
	uc, isUnix := conn.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var cred *unix.Ucred
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil || serr != nil || cred == nil {
		return 0, 0, false
	}
	return cred.Uid, int(cred.Pid), true
}
