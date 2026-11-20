//go:build linux || darwin

package keycache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// prepareTarget makes target, below base (the home or the workspace), a
// real directory a cache can be shown at, walking it one component at a
// time by file descriptor and never following a symlink. A missing
// component is created for a copy; for an overlay the walk stops there
// (the jail creates the rest), so a plain restore writes nothing in the
// home. A symlink refuses: a link there is deliberate (a
// kept cache dir's, persist: cache) and is never replaced. Any other
// non-directory (a file planted where the cache goes) is moved aside, to
// "<name>.conductor-moved-<random>" beside it, and a directory made in its
// place. The target may hold content — a kept home's own downloads from a
// launch that missed, or anything planted there: an overlay (replace
// false) leaves it untouched and shadows it for the launch; a copy
// (replace true) removes it first, so what the launch sees is the cache.
// A cache never depends on the target being empty.
func prepareTarget(base, target string, replace bool) error {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("%s is not inside %s", target, base)
	}
	dfd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", base, err)
	}
	defer func() { unix.Close(dfd) }()
	cur := base
	parts := strings.Split(rel, string(filepath.Separator))
	for i, c := range parts {
		cur = filepath.Join(cur, c)
		var st unix.Stat_t
		err := unix.Fstatat(dfd, c, &st, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case err == unix.ENOENT && !replace:
			return nil
		case err == unix.ENOENT:
			if err := unix.Mkdirat(dfd, c, 0o755); err != nil && err != unix.EEXIST {
				return fmt.Errorf("%s: %w", cur, err)
			}
		case err != nil:
			return fmt.Errorf("%s: %w", cur, err)
		case st.Mode&unix.S_IFMT == unix.S_IFLNK:
			return fmt.Errorf("%s is a symlink", cur)
		case st.Mode&unix.S_IFMT != unix.S_IFDIR:
			if err := unix.Renameat(dfd, c, dfd, c+".conductor-moved-"+randHex()); err != nil {
				return fmt.Errorf("move %s aside: %w", cur, err)
			}
			if err := unix.Mkdirat(dfd, c, 0o755); err != nil {
				return fmt.Errorf("%s: %w", cur, err)
			}
		case replace && i == len(parts)-1:
			if err := removeAt(dfd, c); err != nil {
				return fmt.Errorf("clear %s: %w", cur, err)
			}
			if err := unix.Mkdirat(dfd, c, 0o755); err != nil {
				return fmt.Errorf("%s: %w", cur, err)
			}
		}
		nfd, err := unix.Openat(dfd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", cur, err)
		}
		unix.Close(dfd)
		dfd = nfd
	}
	return nil
}

// removeAt removes name in the directory dfd and everything under it,
// never following a symlink; a read-only directory (a Go module cache's)
// is made writable first.
func removeAt(dfd int, name string) error {
	uerr := unix.Unlinkat(dfd, name, 0)
	if uerr == nil || uerr == unix.ENOENT {
		return nil
	}
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOTDIR || err == unix.ELOOP {
			return uerr
		}
		return err
	}
	_ = unix.Fchmod(fd, 0o700)
	f := os.NewFile(uintptr(fd), name)
	names, err := f.Readdirnames(-1)
	if err != nil {
		f.Close()
		return err
	}
	for _, n := range names {
		if err := removeAt(fd, n); err != nil {
			f.Close()
			return err
		}
	}
	f.Close()
	return unix.Unlinkat(dfd, name, unix.AT_REMOVEDIR)
}
