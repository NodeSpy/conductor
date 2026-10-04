//go:build unix

package plugin

import (
	"os"
	"syscall"
)

// lockFileBlocking takes an exclusive flock(2) on f, blocking until held.
func lockFileBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases f's flock(2).
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
