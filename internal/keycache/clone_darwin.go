//go:build darwin

package keycache

import (
	"os"
	"path/filepath"

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

// cloneFd clones the open file src to dst (absent) copy-on-write
// (fclonefileat); false when the volume cannot, with nothing left.
func cloneFd(src int, dst string, mode os.FileMode) bool {
	dir, err := unix.Open(filepath.Dir(dst), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(dir)
	if err := unix.Fclonefileat(src, dir, filepath.Base(dst), unix.CLONE_NOFOLLOW); err != nil {
		_ = os.Remove(dst)
		return false
	}
	_ = os.Chmod(dst, mode)
	return true
}

// cloneTreeAt clones the tree src (a published version's: conductor's
// own) to name (absent) in the directory dfd in one clonefileat(2): APFS
// copies directories recursively, sharing every file's blocks. The
// destination is named under dfd, never by a path through the home.
func cloneTreeAt(src string, dfd int, name string) bool {
	return unix.Clonefileat(unix.AT_FDCWD, src, dfd, name, unix.CLONE_NOFOLLOW) == nil
}

// cloneFileAt clones the open file src to name (absent) in dfd
// (fclonefileat); false when the volume cannot, with nothing left.
func cloneFileAt(src, dfd int, name string, mode uint32) bool {
	if err := unix.Fclonefileat(src, dfd, name, unix.CLONE_NOFOLLOW); err != nil {
		_ = unix.Unlinkat(dfd, name, 0)
		return false
	}
	_ = unix.Fchmodat(dfd, name, mode, unix.AT_SYMLINK_NOFOLLOW)
	return true
}
