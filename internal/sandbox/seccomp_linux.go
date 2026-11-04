//go:build linux

package sandbox

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The plugin jail's exec filter (EnterOpts.NoExecBase). Mount flags make
// every writable place noexec; what they cannot reach is a program that
// lives in no mount: a memfd (memfd_create, then execve of
// /proc/self/fd/N or execveat of the fd). vm.memfd_noexec would cover it,
// but that sysctl is not writable from an unprivileged user namespace. So
// the payload runs with no_new_privs and a seccomp filter that refuses, with
// EPERM:
//
//   - memfd_create unless it asks for MFD_NOEXEC_SEAL (a memfd that can
//     never be made executable — shared memory keeps working);
//   - execveat with AT_EMPTY_PATH (executing an open fd, whatever it is);
//   - every syscall of a foreign ABI (i386 / x32 on amd64, arm32 on
//     arm64), whose numbers the checks above do not cover.
//
// Everything else is allowed. The filter is built for amd64 and arm64;
// elsewhere ExecFilterSupported is false and the jail runs without it.

const (
	mfdNoexecSeal = 0x0008 // MFD_NOEXEC_SEAL (Linux 6.3)
	x32SyscallBit = 0x40000000
)

type execFilterArch struct {
	audit         uint32
	execveat      uint32
	memfdCreate   uint32
	x32Compatible bool // amd64: syscall numbers with the x32 bit set are another ABI
}

var execFilterArches = map[string]execFilterArch{
	"amd64": {audit: unix.AUDIT_ARCH_X86_64, execveat: 322, memfdCreate: 319, x32Compatible: true},
	"arm64": {audit: unix.AUDIT_ARCH_AARCH64, execveat: 281, memfdCreate: 279},
}

// ExecFilterSupported reports whether this build installs the exec filter.
func ExecFilterSupported() bool {
	_, ok := execFilterArches[runtime.GOARCH]
	return ok
}

// execFilter is the BPF program for arch.
func execFilter(a execFilterArch) []unix.SockFilter {
	const (
		offNr    = 0
		offArch  = 4
		offArgs  = 16
		retAllow = unix.SECCOMP_RET_ALLOW
		retEPERM = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	)
	ld := func(off uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: off}
	}
	jeq := func(k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: k, Jt: jt, Jf: jf}
	}
	jge := func(k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: k, Jt: jt, Jf: jf}
	}
	jset := func(k uint32, jt, jf uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: k, Jt: jt, Jf: jf}
	}
	ret := func(k uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: k} }

	// Jumps are relative to the next instruction; the two returns sit at
	// the end, ALLOW then EPERM. Built back to front would be shorter to
	// compute; spelled out here so each check reads in order.
	var p []unix.SockFilter
	p = append(p, ld(offArch))
	// [1] foreign ABI → EPERM (patched below)
	p = append(p, jeq(a.audit, 0, 0))
	p = append(p, ld(offNr))
	if a.x32Compatible {
		p = append(p, jge(x32SyscallBit, 0, 0)) // → EPERM (patched)
	}
	iExec := len(p)
	p = append(p, jeq(a.execveat, 0, 0)) // jf → memfd check (patched)
	p = append(p, ld(offArgs+4*8))       // args[4], low word (little-endian)
	iFlags := len(p)
	p = append(p, jset(unix.AT_EMPTY_PATH, 0, 0)) // jt → EPERM, jf → ALLOW
	iMemfd := len(p)
	p = append(p, jeq(a.memfdCreate, 0, 0)) // jf → ALLOW
	p = append(p, ld(offArgs+1*8))          // args[1] (flags), low word
	iSeal := len(p)
	p = append(p, jset(mfdNoexecSeal, 0, 0)) // jt → ALLOW, jf → EPERM
	iAllow := len(p)
	p = append(p, ret(retAllow))
	iDeny := len(p)
	p = append(p, ret(retEPERM))

	to := func(from, target int) uint8 { return uint8(target - from - 1) }
	p[1].Jt, p[1].Jf = 0, to(1, iDeny)
	if a.x32Compatible {
		p[3].Jt, p[3].Jf = to(3, iDeny), 0
	}
	p[iExec].Jt, p[iExec].Jf = 0, to(iExec, iMemfd)
	p[iFlags].Jt, p[iFlags].Jf = to(iFlags, iDeny), to(iFlags, iAllow)
	p[iMemfd].Jt, p[iMemfd].Jf = 0, to(iMemfd, iAllow)
	p[iSeal].Jt, p[iSeal].Jf = to(iSeal, iAllow), to(iSeal, iDeny)
	return p
}

// restrictExec sets no_new_privs and installs the exec filter on every
// thread of this process (the payload it starts next inherits both). On an
// architecture without a filter it sets no_new_privs only.
func restrictExec() error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	a, ok := execFilterArches[runtime.GOARCH]
	if !ok {
		return nil
	}
	prog := execFilter(a)
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	// TSYNC: Go runs on several threads and forks the payload from any of
	// them; the filter (and no_new_privs) must be on all.
	r, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	if errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	if r != 0 {
		return fmt.Errorf("seccomp: thread %d could not be synchronized", r)
	}
	return nil
}
