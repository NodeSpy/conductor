package agentsandbox

import "golang.org/x/sys/unix"

// chmodDirNoFollow gives name in dirfd mode 0700, never through a symlink.
func chmodDirNoFollow(dirfd int, name string) error {
	return unix.Fchmodat(dirfd, name, 0o700, unix.AT_SYMLINK_NOFOLLOW)
}
