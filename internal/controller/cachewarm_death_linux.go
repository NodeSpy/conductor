//go:build linux

package controller

import (
	"runtime"
	"syscall"
)

// warmDeathSignal makes a warm's sandbox die with conductor (SIGKILL), so
// a crashed daemon leaves nothing writing the scratch its next start
// removes. The kernel sends the signal when the thread that started the
// child exits, so the caller stays on a locked thread until the child is
// gone; unlock releases it.
func warmDeathSignal(a *syscall.SysProcAttr) (unlock func()) {
	runtime.LockOSThread()
	a.Pdeathsig = syscall.SIGKILL
	return runtime.UnlockOSThread
}
