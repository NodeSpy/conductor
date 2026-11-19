//go:build linux || darwin

package keycache

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// copyIn clones (or, under CopyMax, copies) a cache path into target,
// below home, replacing what the target held. Its version is pinned by the
// caller.
//
// The home is the agent's, and another launch's agent may run in it while
// this restores (a kept home, a resume): any directory on the way may be
// swapped for a symlink at any moment. So nothing here goes by path below
// the home. The walk to target's parent is by file descriptor, never
// following a symlink (openParent), and everything after goes through
// that parent's descriptor: the copy is built in a staging directory made
// in the parent (each directory made with mkdirat and opened O_NOFOLLOW,
// each file created O_EXCL|O_NOFOLLOW under its directory's descriptor;
// on macOS one clonefileat into the parent), the old target is removed
// with unlinkat, and the staging directory renamed onto it with renameat.
// A swap anywhere above lands the writes in the directory the walk opened
// (still the home's) or fails them; it never sends them elsewhere.
func (m *Manager) copyIn(home, lower, target string) error {
	pfd, leaf, err := openParent(home, target)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	var st unix.Stat_t
	switch err := unix.Fstatat(pfd, leaf, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == unix.ENOENT:
	case err != nil:
		return fmt.Errorf("%s: %w", target, err)
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		// A link there is deliberate (a kept cache dir's, persist: cache):
		// never replaced.
		return fmt.Errorf("%s is a symlink", target)
	}
	stage := ".conductor-restore-" + randHex()
	if m.testHook != nil {
		m.testHook("copy-parent")
	}
	if !cloneTreeAt(lower, pfd, stage) {
		max := m.CopyMax
		if max <= 0 {
			max = 1 << 30
		}
		if err := copyTreeAt(lower, pfd, stage, &max); err != nil {
			_ = removeAt(pfd, stage)
			return err
		}
	}
	if err := clearAt(pfd, leaf); err != nil {
		_ = removeAt(pfd, stage)
		return fmt.Errorf("clear %s: %w", target, err)
	}
	if err := unix.Renameat(pfd, stage, pfd, leaf); err != nil {
		_ = removeAt(pfd, stage)
		return fmt.Errorf("%s: %w", target, err)
	}
	return nil
}

// clearAt makes name in dfd absent: a directory is removed (by fd, never
// following a symlink), anything else that is not a symlink moved aside
// to "<name>.conductor-moved-<random>", a symlink unlinked (it was not one
// when copyIn looked: the agent made it since, and only the link goes).
func clearAt(dfd int, name string) error {
	var st unix.Stat_t
	switch err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == unix.ENOENT:
		return nil
	case err != nil:
		return err
	case st.Mode&unix.S_IFMT == unix.S_IFDIR:
		return removeAt(dfd, name)
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		return unix.Unlinkat(dfd, name, 0)
	default:
		return unix.Renameat(dfd, name, dfd, name+".conductor-moved-"+randHex())
	}
}

// openParent opens target's parent directory, below base, walking one
// component at a time by file descriptor from base and never following a
// symlink; a missing directory is made (mkdirat), a non-directory in the
// way moved aside and a directory made in its place. It returns the
// parent's descriptor (the caller closes it) and target's last component.
func openParent(base, target string) (int, string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return -1, "", fmt.Errorf("%s is not inside %s", target, base)
	}
	dfd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", fmt.Errorf("%s: %w", base, err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := base
	for _, c := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, c)
		var st unix.Stat_t
		err := unix.Fstatat(dfd, c, &st, unix.AT_SYMLINK_NOFOLLOW)
		switch {
		case err == unix.ENOENT:
			if err := unix.Mkdirat(dfd, c, 0o755); err != nil && err != unix.EEXIST {
				unix.Close(dfd)
				return -1, "", fmt.Errorf("%s: %w", cur, err)
			}
		case err != nil:
			unix.Close(dfd)
			return -1, "", fmt.Errorf("%s: %w", cur, err)
		case st.Mode&unix.S_IFMT == unix.S_IFLNK:
			unix.Close(dfd)
			return -1, "", fmt.Errorf("%s is a symlink", cur)
		case st.Mode&unix.S_IFMT != unix.S_IFDIR:
			if err := unix.Renameat(dfd, c, dfd, c+".conductor-moved-"+randHex()); err != nil {
				unix.Close(dfd)
				return -1, "", fmt.Errorf("move %s aside: %w", cur, err)
			}
			if err := unix.Mkdirat(dfd, c, 0o755); err != nil {
				unix.Close(dfd)
				return -1, "", fmt.Errorf("%s: %w", cur, err)
			}
		}
		// O_NOFOLLOW: a symlink swapped in since the stat refuses here.
		nfd, err := unix.Openat(dfd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(dfd)
		if err != nil {
			return -1, "", fmt.Errorf("%s: %w (a symlink is never followed)", cur, err)
		}
		dfd = nfd
	}
	return dfd, parts[len(parts)-1], nil
}

// copyTreeAt copies src (a published version's directory: conductor's own,
// read by path) to name in the directory dfd, creating everything under
// dfd by descriptor: each directory made with mkdirat and opened
// O_NOFOLLOW, each file created O_EXCL|O_NOFOLLOW, each symlink made with
// symlinkat. A regular file is cloned where the filesystem can; budget is
// what may still be copied (clones are not counted), past it errCopyCap.
func copyTreeAt(src string, dfd int, name string, budget *int64) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := unix.Mkdirat(dfd, name, uint32(fi.Mode().Perm()|0o700)); err != nil {
		return err
	}
	cfd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(cfd)
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(src, e.Name())
		switch {
		case e.IsDir():
			if err := copyTreeAt(p, cfd, e.Name(), budget); err != nil {
				return err
			}
		case e.Type()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := unix.Symlinkat(l, cfd, e.Name()); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := copyFileTo(p, cfd, e.Name(), budget); err != nil {
				return err
			}
		}
		// devices, sockets, fifos: never cached
	}
	return nil
}

// copyFileTo copies the regular file src to name (absent) in dfd.
func copyFileTo(src string, dfd int, name string, budget *int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	mode := uint32(fi.Mode().Perm() | 0o200)
	if cloneFileAt(int(in.Fd()), dfd, name, mode) {
		return nil
	}
	if *budget -= fi.Size(); *budget < 0 {
		return errCopyCap
	}
	ofd, err := unix.Openat(dfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	out := os.NewFile(uintptr(ofd), name)
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
