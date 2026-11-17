package agentsandbox

import (
	"errors"

	"golang.org/x/sys/unix"
)

// oSearch is macOS's O_SEARCH (O_EXEC|O_DIRECTORY): a directory its owner
// may search but not read opens with it.
const oSearch = 0x40000000 | unix.O_DIRECTORY

// errNoSafeChmod: a directory its owner can neither read nor search cannot
// be opened on macOS, and chmodding it by name could chmod whatever was
// swapped in under that name since its stat (a hard link to a file outside
// the home): it is refused, not chmodded.
var errNoSafeChmod = errors.New("its owner cannot open it, and macOS can only chmod it by name: refused")

// openDirForRemoval opens name in dirfd for reading its entries and removing
// them, owner-writable. A directory is only ever chmodded through an fd
// opened O_NOFOLLOW and checked to be the one stat'ed (dev/ino) — never by
// name.
func openDirForRemoval(dirfd int, name string, st *unix.Stat_t) (int, error) {
	open := func(flags int) (int, error) {
		fd, err := unix.Openat(dirfd, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, err
		}
		var cur unix.Stat_t
		if err := unix.Fstat(fd, &cur); err != nil || cur.Mode&unix.S_IFMT != unix.S_IFDIR || !sameInode(&cur, st) {
			unix.Close(fd)
			return -1, errChanged
		}
		return fd, nil
	}
	fd, err := open(unix.O_RDONLY | unix.O_DIRECTORY)
	if errors.Is(err, unix.EACCES) {
		// Searchable, not readable: made readable through a search fd.
		sfd, serr := open(oSearch)
		if serr != nil {
			if errors.Is(serr, unix.EACCES) {
				return -1, errNoSafeChmod
			}
			return -1, serr
		}
		cerr := unix.Fchmod(sfd, uint32(st.Mode&0o7777)|0o700)
		unix.Close(sfd)
		if cerr != nil {
			return -1, cerr
		}
		fd, err = open(unix.O_RDONLY | unix.O_DIRECTORY)
	}
	if err != nil {
		return -1, err
	}
	var cur unix.Stat_t
	if err := unix.Fstat(fd, &cur); err == nil && cur.Mode&0o700 != 0o700 {
		if err := unix.Fchmod(fd, uint32(cur.Mode&0o7777)|0o700); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

// isMountRoot reports whether name in dirfd is a mount's root: macOS has no
// subvolumes and no bind mounts, so another st_dev is a mount (known=false
// asks the caller to compare st_dev).
func isMountRoot(int, string) (mount, known bool) { return false, false }
