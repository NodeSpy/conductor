//go:build darwin

package jail

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads the connecting process's uid off the socket (LOCAL_PEERCRED).
func peerUID(conn net.Conn) (uint32, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Xucred
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); cerr != nil || serr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
