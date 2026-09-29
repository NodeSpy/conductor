//go:build linux

package jail

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads the connecting process's uid off the socket (SO_PEERCRED),
// translated into the daemon's user namespace — a jailed agent's nested
// namespaces report the operator's real uid here.
func peerUID(conn net.Conn) (uint32, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil || serr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
