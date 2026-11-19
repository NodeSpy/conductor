//go:build linux || darwin

package keycache

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes p's lock exclusively, waiting for it; the release closes
// it. A lock that cannot be taken is no lock (the in-process mutex still
// holds).
func lockFile(p string) (release func()) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return func() {}
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
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

// LockShared holds p's lock shared until the release: a launch holds its
// entry's so long as it runs. The fd is close-on-exec: the agent never
// holds it.
func LockShared(p string) (release func(), err error) {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return func() {}, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_SH)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return func() {}, err
	}
	// A sweep removes an ended launch's entry holding its lock: one taken
	// after is on a file that is gone, and its layers with it.
	var held, now unix.Stat_t
	if unix.Fstat(int(f.Fd()), &held) != nil || unix.Stat(p, &now) != nil || held.Ino != now.Ino || held.Dev != now.Dev || held.Nlink == 0 {
		f.Close()
		return func() {}, errSwept
	}
	return func() { f.Close() }, nil
}

// tryLock takes p's lock exclusively if no one holds it. ok is false
// while someone does; a lock file that is gone is no one's (ok, nothing
// to release).
func tryLock(p string) (release func(), ok bool) {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return func() {}, os.IsNotExist(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false
	}
	return func() { f.Close() }, true
}

// lockHeld reports p's lock held by someone (a live launch).
func lockHeld(p string) bool {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err == unix.EWOULDBLOCK
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}
