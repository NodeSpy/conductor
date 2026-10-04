//go:build !unix

package plugin

import "os"

// lockFileBlocking is a documented no-op on a non-unix build (flock(2) has
// no portable equivalent wired up here): LockInstallState degrades to no
// cross-process synchronization on this platform rather than failing the
// caller. See filelock.go's doc comment.
func lockFileBlocking(f *os.File) error { return errUnsupportedFlock }

// unlockFile is never called when lockFileBlocking always "fails" above
// (LockInstallState treats that as "use the no-op lock"), but is defined for
// symmetry and in case a future platform wants a real implementation here.
func unlockFile(f *os.File) error { return nil }

var errUnsupportedFlock = &unsupportedFlockError{}

type unsupportedFlockError struct{}

func (*unsupportedFlockError) Error() string { return "flock: not supported on this platform" }
