//go:build darwin

package repocache

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile clones src to dst (absent) copy-on-write (APFS clonefile);
// false when the volume cannot, with nothing left.
func cloneFile(src, dst string, mode os.FileMode) (bool, error) {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		_ = os.Remove(dst)
		return false, nil
	}
	return true, nil
}

// cloneDir clones a whole tree (dst absent) in one clonefile(2): APFS
// copies directories recursively, sharing every file's blocks.
func cloneDir(src, dst string) bool {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) == nil
}

// OverlayUsable: macOS has no overlay mount.
func OverlayUsable(lowerRoot, upperRoot string) (bool, string) {
	return false, "no overlay mounts on macOS"
}
