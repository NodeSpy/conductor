//go:build darwin

package memory

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerInfo reads the process on the other end of a unix socket: its uid
// (LOCAL_PEERCRED), its pid (LOCAL_PEERPID; macOS has no single getsockopt
// carrying both) and its start time, in the units skill's process-tree
// check uses (sysctl kern.proc.pid's p_starttime, in microseconds). Without
// it a sandboxed launch's tree-bound token could not be spent on macOS.
// The zero Peer (not Valid) when any of it cannot be read.
func peerInfo(conn net.Conn) Peer {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}
	}
	var cred *unix.Xucred
	pid := 0
	var serr, perr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		pid, perr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); cerr != nil || serr != nil || perr != nil || cred == nil || pid <= 0 {
		return Peer{}
	}
	return Peer{PID: pid, StartTime: procStartTime(pid), UID: cred.Uid, Valid: true}
}

// procStartTime is pid's start time (0: unreadable, which the tree check
// treats as unverifiable, never as a match).
func procStartTime(pid int) uint64 {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil {
		return 0
	}
	return uint64(kp.Proc.P_starttime.Sec)*1e6 + uint64(kp.Proc.P_starttime.Usec)
}
