//go:build linux

package memory

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// peerInfo reads the connecting process's identity off the unix socket
// (SO_PEERCRED) plus its /proc start time — what the skill broker binds a
// claimed session to, so a copied token is useless from any other process.
func peerInfo(conn net.Conn) Peer {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}
	}
	var cred *syscall.Ucred
	var serr error
	if cerr := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); cerr != nil || serr != nil || cred == nil || cred.Pid == 0 {
		return Peer{}
	}
	return Peer{PID: int(cred.Pid), StartTime: procStartTime(int(cred.Pid)), Valid: true}
}

// procStartTime reads /proc/<pid>/stat field 22 (starttime, clock ticks since
// boot) — the PID-reuse guard. 0 when unreadable (binding then holds on PID
// alone).
func procStartTime(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The comm field (2) is parenthesized and may contain spaces; parse from
	// the LAST ')' so a hostile process name can't shift the fields.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is stat field 3 (state); starttime is stat field 22.
	if len(fields) < 20 {
		return 0
	}
	v, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	return v
}
