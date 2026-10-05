//go:build unix

package plugin

import (
	"errors"
	"os"
	"syscall"
)

// noFollowFlag refuses to open the lockfile if it is a symlink (finding 6,
// hardening) — see openLockFile's doc comment.
const noFollowFlag = syscall.O_NOFOLLOW

// isSymlinkRefusalErr reports whether err is what O_NOFOLLOW produces when
// the path is a symlink (ELOOP on every unix this builds for).
func isSymlinkRefusalErr(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}

// lockFileBlocking takes an exclusive flock(2) on f, blocking until held.
func lockFileBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// lockFileNonBlocking takes an exclusive flock(2) on f, returning
// immediately with an error satisfying isLockHeldErr when another holder
// has it.
func lockFileNonBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// isLockHeldErr reports whether err is flock(2)'s "already held by someone
// else" result for a LOCK_NB attempt, as opposed to some other failure (an
// unsupported filesystem, say) that should fall back to the no-op degrade.
func isLockHeldErr(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// unlockFile releases f's flock(2).
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
