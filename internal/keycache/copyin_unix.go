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

// copyFromHome copies the directory home/rel into dst (absent) for a
// publish. A home is the agent's: anything in it may be a symlink, a FIFO or
// change under the walk (a kept home another launch still uses). So the
// walk is by file descriptor from the home down, never following a symlink
// (a symlink on the way to rel refuses it; one inside is copied as a link),
// a file is opened without following and without blocking and must be a
// regular file once open, and devices, FIFOs and sockets are skipped. A
// regular file is cloned where the filesystem can (reflink, APFS), else
// copied — never hard-linked, so nothing in the home shares an inode with
// the store. budget is what may still be written (allocated bytes); past
// it the copy stops with errCopyCap.
func copyFromHome(home, rel, dst string, budget *int64) error {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", home, err)
	}
	for _, c := range strings.Split(rel, "/") {
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return fmt.Errorf("~/%s: %w (a symlink is never followed)", rel, err)
		}
		fd = nfd
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return err
	}
	if err := os.Mkdir(dst, os.FileMode(st.Mode&0o777)|0o700); err != nil {
		unix.Close(fd)
		return err
	}
	return copyDirFd(fd, dst, budget)
}

// copyDirFd copies the directory open at fd into dst (made); it closes fd.
func copyDirFd(fd int, dst string, budget *int64) error {
	d := os.NewFile(uintptr(fd), dst)
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // gone under the walk
		}
		t := filepath.Join(dst, name)
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			cfd, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				continue // replaced under the walk by a non-directory
			}
			if err := os.Mkdir(t, os.FileMode(st.Mode&0o777)|0o700); err != nil {
				unix.Close(cfd)
				return err
			}
			if err := copyDirFd(cfd, t, budget); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := copyFileAt(fd, name, t, budget); err != nil {
				return err
			}
		case unix.S_IFLNK:
			buf := make([]byte, 4096)
			n, err := unix.Readlinkat(fd, name, buf)
			if err != nil || n >= len(buf) {
				continue
			}
			if err := os.Symlink(string(buf[:n]), t); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFileAt copies the regular file name in the directory fd to dst.
func copyFileAt(fd int, name, dst string, budget *int64) error {
	sfd, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil // gone, or replaced by a symlink, under the walk
	}
	src := os.NewFile(uintptr(sfd), name)
	defer src.Close()
	var st unix.Stat_t
	if err := unix.Fstat(sfd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil // a FIFO or a device swapped in: never read
	}
	if *budget -= int64(st.Blocks) * 512; *budget < 0 {
		return errCopyCap
	}
	mode := os.FileMode(st.Mode&0o777) | 0o200
	if cloneFd(sfd, dst, mode) {
		return nil
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(src, st.Size)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
