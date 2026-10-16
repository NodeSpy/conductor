//go:build unix

package gitpush

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The agent can write everything under its workspace. conductor reads from
// it only through these: root is a path conductor chose (the workspace, a
// linked worktree's git dir) and opened as is; every component below it is
// opened with O_NOFOLLOW, so a symlink the agent planted — .git/HEAD →
// ~/.ssh/id_ed25519, objects/pack → another repository — is refused rather
// than followed.

// openDirUnder opens root/rel (a directory), never following a symlink below root.
func openDirUnder(root, rel string) (int, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, c := range splitRel(rel) {
		nfd, err := unix.Openat(fd, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, fmt.Errorf("%s: %w", filepath.Join(root, rel), err)
		}
		fd = nfd
	}
	return fd, nil
}

// readUnder reads the regular file root/rel (at most max bytes).
func readUnder(root, rel string, max int64) ([]byte, error) {
	dir, name := filepath.Split(rel)
	dfd, err := openDirUnder(root, dir)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dfd)
	return readAt(dfd, name, max)
}

func readAt(dfd int, name string, max int64) ([]byte, error) {
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// writeUnder atomically replaces root/rel with b (a temp file renamed in the
// same directory), never following a symlink below root.
func writeUnder(root, rel string, b []byte, perm uint32) error {
	dir, name := filepath.Split(rel)
	dfd, err := openDirUnder(root, dir)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	tmp := "." + name + ".conductor-tmp"
	_ = unix.Unlinkat(dfd, tmp, 0)
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := unix.Renameat(dfd, tmp, dfd, name); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	return nil
}

// mkdirUnder makes root/rel/name (one level) if absent, refusing a symlink.
func mkdirUnder(root, rel, name string) error {
	dfd, err := openDirUnder(root, rel)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Mkdirat(dfd, name, 0o755); err != nil && err != unix.EEXIST {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(dfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a directory", filepath.Join(root, rel, name))
	}
	return nil
}

func readDirNames(fd int) ([]string, error) {
	dfd, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dfd), "dir")
	defer f.Close()
	return f.Readdirnames(-1)
}

// maxSnapshotBytes bounds one snapshot: a clone's OWN objects are what its
// agent created, not the repository (that is borrowed from the base).
const maxSnapshotBytes = 2 << 30

// snapshotObjects copies a per-dispatch clone's own object store
// (<ws>/.git/objects) into dst, a directory only conductor writes, for one
// push. Only regular files are copied — loose objects and pack-*.pack/.idx —
// and objects/info (alternates) is never read: an alternates line or a
// symlinked pack the agent planted cannot point conductor's git at another
// repository on this machine.
func snapshotObjects(ws, dst string) error {
	ofd, err := openDirUnder(ws, ".git/objects")
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	defer unix.Close(ofd)
	if err := os.MkdirAll(filepath.Join(dst, "pack"), 0o700); err != nil {
		return err
	}
	var total int64
	names, err := readDirNames(ofd)
	if err != nil {
		return err
	}
	for _, n := range names {
		switch {
		case n == "pack":
			pfd, err := unix.Openat(ofd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			files, _ := readDirNames(pfd)
			for _, f := range files {
				if strings.HasPrefix(f, "pack-") && (strings.HasSuffix(f, ".pack") || strings.HasSuffix(f, ".idx")) {
					if err := copyAt(pfd, f, filepath.Join(dst, "pack", f), &total); err != nil {
						unix.Close(pfd)
						return err
					}
				}
			}
			unix.Close(pfd)
		case len(n) == 2 && isHex(n):
			sfd, err := unix.Openat(ofd, n, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			files, _ := readDirNames(sfd)
			for _, f := range files {
				if len(f) >= 38 && isHex(f) {
					if err := os.MkdirAll(filepath.Join(dst, n), 0o700); err != nil {
						unix.Close(sfd)
						return err
					}
					if err := copyAt(sfd, f, filepath.Join(dst, n, f), &total); err != nil {
						unix.Close(sfd)
						return err
					}
				}
			}
			unix.Close(sfd)
		}
	}
	return nil
}

// copyAt copies the regular file name (never a symlink) under dirfd to dst.
func copyAt(dirfd int, name, dst string, total *int64) error {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil // a symlink (ELOOP) or a vanished file: not one of the clone's objects
	}
	src := os.NewFile(uintptr(fd), name)
	defer src.Close()
	fi, err := src.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	*total += fi.Size()
	if *total > maxSnapshotBytes {
		return fmt.Errorf("snapshot: the clone's own objects exceed %d bytes", int64(maxSnapshotBytes))
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(src, fi.Size())); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
