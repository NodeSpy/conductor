//go:build linux || darwin

package agentsandbox

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// lockHomeShared holds a shared lock on a per-dispatch home's lock file for
// as long as a launch runs in it: the reclaim never removes a home a live
// agent is using, and a launcher that dies drops its lock with it. The fd is
// close-on-exec, so the agent never holds it. Best-effort: a lock that
// cannot be taken leaves the launch unprotected, never refused.
func lockHomeShared(path string) func() {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return func() {}
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_SH)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return func() {}
	}
	return func() { f.Close() }
}

// tryLockHomeShared is lockHomeShared without waiting: the daemon's hold,
// taken while it holds homesMu, never blocks on another process's lock. A
// lock held exclusively (only a reclaim takes it so, under homesMu) is
// retried briefly, then left untaken.
func tryLockHomeShared(path string) func() {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return func() {}
	}
	for i := 0; i < 20; i++ {
		err = unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)
		if err == nil {
			return func() { f.Close() }
		}
		if err != unix.EWOULDBLOCK && err != unix.EINTR {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.Close()
	return func() {}
}

// tryLockHomeExclusive takes a home's lock exclusively, without waiting:
// free=false means a launch holds it now. A lock file that cannot be opened
// reads as free — the home has no launch that took one.
func tryLockHomeExclusive(path string) (release func(), free bool) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return func() {}, true
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false
	}
	return func() { f.Close() }, true
}
