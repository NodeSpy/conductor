//go:build linux

package sandbox

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

// runBPF interprets the classic-BPF subset execFilter emits over one
// seccomp_data record.
func runBPF(t *testing.T, prog []unix.SockFilter, data []byte) uint32 {
	t.Helper()
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			a = binary.LittleEndian.Uint32(data[in.K:])
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			pc += int(map[bool]uint8{true: in.Jt, false: in.Jf}[a == in.K])
		case unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K:
			pc += int(map[bool]uint8{true: in.Jt, false: in.Jf}[a >= in.K])
		case unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
			pc += int(map[bool]uint8{true: in.Jt, false: in.Jf}[a&in.K != 0])
		case unix.BPF_RET | unix.BPF_K:
			return in.K
		default:
			t.Fatalf("unexpected BPF op %#x at %d", in.Code, pc)
		}
	}
	t.Fatal("the program ran off its end")
	return 0
}

func seccompData(nr, arch uint32, args ...uint64) []byte {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], nr)
	binary.LittleEndian.PutUint32(b[4:], arch)
	for i, v := range args {
		binary.LittleEndian.PutUint64(b[16+8*i:], v)
	}
	return b
}

// The exec filter's decisions, per architecture: execveat of an fd, an
// executable memfd and any foreign-ABI call are EPERM; everything else
// (a path execveat, a sealed memfd, ordinary calls) is allowed.
func TestExecFilterDecisions(t *testing.T) {
	const eperm = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	for name, a := range execFilterArches {
		prog := execFilter(a)
		foreign := uint32(unix.AUDIT_ARCH_I386)
		if name == "arm64" {
			foreign = unix.AUDIT_ARCH_ARM
		}
		for _, tc := range []struct {
			what string
			data []byte
			want uint32
		}{
			{"execveat AT_EMPTY_PATH", seccompData(a.execveat, a.audit, 3, 0, 0, 0, unix.AT_EMPTY_PATH), eperm},
			{"execveat AT_EMPTY_PATH|AT_SYMLINK_NOFOLLOW", seccompData(a.execveat, a.audit, 3, 0, 0, 0, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW), eperm},
			{"execveat by path", seccompData(a.execveat, a.audit, 0xffffff9c, 0, 0, 0, 0), unix.SECCOMP_RET_ALLOW},
			{"memfd_create(0)", seccompData(a.memfdCreate, a.audit, 0, 0), eperm},
			{"memfd_create(MFD_CLOEXEC)", seccompData(a.memfdCreate, a.audit, 0, unix.MFD_CLOEXEC), eperm},
			{"memfd_create(MFD_NOEXEC_SEAL)", seccompData(a.memfdCreate, a.audit, 0, mfdNoexecSeal|unix.MFD_CLOEXEC), unix.SECCOMP_RET_ALLOW},
			{"an ordinary call", seccompData(0, a.audit), unix.SECCOMP_RET_ALLOW},
			{"a foreign ABI", seccompData(11, foreign), eperm},
		} {
			if got := runBPF(t, prog, tc.data); got != tc.want {
				t.Errorf("%s %s: %#x, want %#x", name, tc.what, got, tc.want)
			}
		}
		if a.x32Compatible {
			if got := runBPF(t, prog, seccompData(x32SyscallBit|59, a.audit)); got != eperm {
				t.Errorf("%s x32 execve: %#x, want EPERM", name, got)
			}
		}
	}
}
