package procexec

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// startSig and startPPID are read in init, which the runtime runs on the
// thread the process started on: the one thread whose parent-death signal is
// the one the parent set.
var startSig, startPPID = deathSig(), os.Getppid()

func deathSig() int {
	var sig int32
	if err := unix.Prctl(unix.PR_GET_PDEATHSIG, uintptr(unsafe.Pointer(&sig)), 0, 0, 0); err != nil {
		return 0
	}
	return int(sig)
}

// armThread locks the calling goroutine to its thread and gives the thread
// the start-up parent-death signal, so the exec'd image inherits it. If the
// parent is already gone, the signal it would have sent is sent now.
func armThread() (unlock func()) {
	runtime.LockOSThread()
	if startSig != 0 {
		_ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(startSig), 0, 0, 0)
		if os.Getppid() != startPPID {
			_ = syscall.Kill(os.Getpid(), syscall.Signal(startSig))
		}
	}
	return runtime.UnlockOSThread
}
