package agentsandbox

import (
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// chmodDirNoFollow gives name in dirfd mode 0700, never through a symlink:
// fchmodat2's AT_SYMLINK_NOFOLLOW, or on a kernel without it the inode
// itself, through an O_PATH fd checked to be a directory.
func chmodDirNoFollow(dirfd int, name string) error {
	err := unix.Fchmodat(dirfd, name, 0o700, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	fd, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.ENOTDIR
	}
	return unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), 0o700)
}
