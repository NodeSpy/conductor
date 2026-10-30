//go:build !linux

package procexec

import "runtime"

// armThread only locks the thread: there is no parent-death signal to carry.
func armThread() (unlock func()) {
	runtime.LockOSThread()
	return runtime.UnlockOSThread
}
