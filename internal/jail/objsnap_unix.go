//go:build unix

package jail

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// maxSnapshotBytes bounds what one snapshot copies: a dispatch clone's OWN
// objects are what its agent created (its commits), not the repository —
// that lives in the base.
const maxSnapshotBytes = 2 << 30

// snapshotObjects copies the dispatch clone's own object store
// (<ws>/.git/objects) into dst, a directory only conductor can write, for
// one git run of conductor's that needs the dispatch's objects (a push, the
// signing check). conductor never points git at the clone's store itself:
// the agent can write it, and git would follow whatever it plants there — an
// objects/info/alternates naming another repository on this machine, or a
// pack dir symlinked to one — reading the operator's files on the agent's
// behalf. Here every path component is opened with O_NOFOLLOW from the
// workspace down, only regular files are copied (loose objects, .pack and
// .idx), and objects/info is never read, so the copy holds the clone's own
// objects and nothing it can point at.
func snapshotObjects(ws, dst string) error {
	wfd, err := unix.Open(ws, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("snapshot: workspace: %w", err)
	}
	defer unix.Close(wfd)
	gfd, err := openDirAt(wfd, ".git")
	if err != nil {
		return fmt.Errorf("snapshot: .git: %w", err)
	}
	defer unix.Close(gfd)
	ofd, err := openDirAt(gfd, "objects")
	if err != nil {
		return fmt.Errorf("snapshot: objects: %w", err)
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
			pfd, err := openDirAt(ofd, n)
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
		case len(n) == 2 && isHex(n+"00000"):
			sfd, err := openDirAt(ofd, n)
			if err != nil {
				continue
			}
			files, _ := readDirNames(sfd)
			for _, f := range files {
				if len(f) >= 38 && isHex(f[:7]) && !strings.Contains(f, ".") {
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
		// info/ (alternates, packs, commit-graph) and anything else: never read.
	}
	return nil
}

func openDirAt(dirfd int, name string) (int, error) {
	return unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

func readDirNames(fd int) ([]string, error) {
	dfd, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dfd), "objects")
	defer f.Close()
	return f.Readdirnames(-1)
}

// copyAt copies the regular file name (never a symlink) under dirfd to dst.
func copyAt(dirfd int, name, dst string, total *int64) error {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil // a symlink (ELOOP) or a vanished file: not an object of the clone's
	}
	src := os.NewFile(uintptr(fd), name)
	defer src.Close()
	fi, err := src.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	*total += fi.Size()
	if *total > maxSnapshotBytes {
		return fmt.Errorf("snapshot: the dispatch clone's own objects exceed %d bytes", int64(maxSnapshotBytes))
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
