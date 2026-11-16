//go:build linux || darwin

package agentsandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// removeTree removes path and everything under it, and returns the disk
// space it held. path is an agent's home: agent-controlled, so hostile.
// Every step is relative to an open directory fd and never follows a
// symlink — a link is unlinked, never its target — and it never descends
// into another filesystem. A directory the agent made read-only (a Go
// module cache: 0555 dirs, 0444 files) is made writable by its owner, on
// its own fd, before its entries go. It carries on past an entry it cannot
// remove and returns the first error.
func removeTree(path string) (int64, error) {
	path = filepath.Clean(path)
	pfd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(pfd)
	var pst unix.Stat_t
	if err := unix.Fstat(pfd, &pst); err != nil {
		return 0, err
	}
	n, err := removeAt(pfd, filepath.Base(path), uint64(pst.Dev), path)
	if errors.Is(err, unix.ENOENT) {
		err = nil
	}
	return n, err
}

// removeAt removes name in dirfd (shown as at in errors).
func removeAt(dirfd int, name string, dev uint64, at string) (int64, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, fmt.Errorf("%s: %w", at, err)
	}
	size := int64(st.Blocks) * 512
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		// Unlinked by name, never opened or chmodded: a symlink or a hard
		// link to a file outside the home loses only this name.
		if err := unix.Unlinkat(dirfd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return 0, fmt.Errorf("%s: %w", at, err)
		}
		return size, nil
	}
	if uint64(st.Dev) != dev {
		// Another filesystem: a mount point is never entered; a btrfs
		// subvolume (its own st_dev, no mount) is the home's and goes.
		if mount, known := isMountRoot(dirfd, name); mount || !known {
			return 0, fmt.Errorf("%s: a mount point, not removed", at)
		}
		dev = uint64(st.Dev)
	}
	// The directory is opened, made writable and checked to be the one
	// stat'ed above (dev/ino) without ever going through a symlink.
	fd, err := openDirForRemoval(dirfd, name, &st)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", at, err)
	}
	d := os.NewFile(uintptr(fd), at)
	names, rerr := d.Readdirnames(-1)
	var first error
	if rerr != nil {
		first = fmt.Errorf("%s: %w", at, rerr)
	}
	for _, n := range names {
		got, err := removeAt(int(d.Fd()), n, dev, filepath.Join(at, n))
		size += got
		if err != nil && first == nil {
			first = err
		}
	}
	d.Close()
	if err := unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) && first == nil {
		first = fmt.Errorf("%s: %w", at, err)
	}
	return size, first
}

// errChanged: the entry is no longer the directory stat'ed (a swap).
var errChanged = errors.New("changed while being removed")

func sameInode(a, b *unix.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }
