//go:build unix

package scratch

import (
	"os"
	"syscall"
)

func lockExclusive(f *os.File, nonBlocking bool) error {
	how := syscall.LOCK_EX
	if nonBlocking {
		how |= syscall.LOCK_NB
	}
	return syscall.Flock(int(f.Fd()), how)
}
