//go:build !unix

package plugin

import "os"

// noFollowFlag is 0 on a non-unix build: no portable O_NOFOLLOW is wired up
// here, matching lockFileBlocking's own no-op degrade below — there is
// nothing for a symlinked lockfile to gain when nothing locks it either.
const noFollowFlag = 0

// isSymlinkRefusalErr never matches on this build: openLockFile's O_NOFOLLOW
// check is a no-op here (see noFollowFlag).
func isSymlinkRefusalErr(error) bool { return false }

// lockFileBlocking is a documented no-op on a non-unix build (flock(2) has
// no portable equivalent wired up here): LockInstallState degrades to no
// cross-process synchronization on this platform rather than failing the
// caller. See filelock.go's doc comment.
func lockFileBlocking(f *os.File) error { return errUnsupportedFlock }

// lockFileNonBlocking mirrors lockFileBlocking's no-op degrade.
func lockFileNonBlocking(f *os.File) error { return errUnsupportedFlock }

// isLockHeldErr is always false here: lockFileNonBlocking's error always
// means "unsupported", never "held by someone else" (isLockHeldErr == false
// is what LockInstallStateCLI reads as "fall back to the no-op degrade").
func isLockHeldErr(error) bool { return false }

// unlockFile is never called when lockFileBlocking always "fails" above
// (LockInstallState treats that as "use the no-op lock"), but is defined for
// symmetry and in case a future platform wants a real implementation here.
func unlockFile(f *os.File) error { return nil }

var errUnsupportedFlock = &unsupportedFlockError{}

type unsupportedFlockError struct{}

func (*unsupportedFlockError) Error() string { return "flock: not supported on this platform" }
