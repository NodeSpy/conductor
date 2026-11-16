package agentsandbox

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// openDirForRemoval opens name in dirfd for reading its entries and removing
// them, owner-writable. Everything goes through one O_PATH|O_NOFOLLOW fd,
// checked to be a directory with st's dev/ino: a swap after the stat (a
// symlink, a hard link to a file outside the home, another directory) is
// refused, and nothing but the checked inode is ever chmodded.
func openDirForRemoval(dirfd int, name string, st *unix.Stat_t) (int, error) {
	pfd, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(pfd)
	var cur unix.Stat_t
	if err := unix.Fstat(pfd, &cur); err != nil {
		return -1, err
	}
	if cur.Mode&unix.S_IFMT != unix.S_IFDIR || !sameInode(&cur, st) {
		return -1, errChanged
	}
	if cur.Mode&0o700 != 0o700 {
		// chmod through the fd's /proc link reaches the fd's own inode.
		if err := unix.Chmod("/proc/self/fd/"+strconv.Itoa(pfd), uint32(cur.Mode&0o7777)|0o700); err != nil {
			return -1, err
		}
	}
	return unix.Openat(pfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// isMountRoot reports whether name in dirfd is a mount's root (known=false:
// the kernel cannot tell).
func isMountRoot(dirfd int, name string) (mount, known bool) {
	var stx unix.Statx_t
	if err := unix.Statx(dirfd, name, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &stx); err != nil {
		return false, false
	}
	if stx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0 {
		return false, false
	}
	return stx.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0, true
}
