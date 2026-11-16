package agentsandbox

import (
	"errors"

	"golang.org/x/sys/unix"
)

// openDirForRemoval opens name in dirfd for reading its entries and removing
// them, owner-writable, never through a symlink. The directory is opened
// (O_NOFOLLOW), checked to be the one stat'ed (dev/ino), and chmodded on its
// own fd. Only a directory its owner cannot open is chmodded by name first
// (AT_SYMLINK_NOFOLLOW) — macOS has no O_PATH — and re-checked after: a swap
// is refused.
func openDirForRemoval(dirfd int, name string, st *unix.Stat_t) (int, error) {
	open := func() (int, error) {
		return unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	fd, err := open()
	if errors.Is(err, unix.EACCES) {
		if err := unix.Fchmodat(dirfd, name, uint32(st.Mode&0o7777)|0o700, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return -1, err
		}
		var cur unix.Stat_t
		if err := unix.Fstatat(dirfd, name, &cur, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return -1, err
		}
		if cur.Mode&unix.S_IFMT != unix.S_IFDIR || !sameInode(&cur, st) {
			return -1, errChanged
		}
		fd, err = open()
	}
	if err != nil {
		return -1, err
	}
	var cur unix.Stat_t
	if err := unix.Fstat(fd, &cur); err != nil || !sameInode(&cur, st) {
		unix.Close(fd)
		return -1, errChanged
	}
	if cur.Mode&0o700 != 0o700 {
		if err := unix.Fchmod(fd, uint32(cur.Mode&0o7777)|0o700); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

// isMountRoot: macOS has no subvolumes; another st_dev is a mount.
func isMountRoot(int, string) (mount, known bool) { return true, true }
