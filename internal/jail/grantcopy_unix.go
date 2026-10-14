//go:build unix

package jail

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// copyNoFollow copies root/rel to dst — every component of rel opened
// without following a symlink, a regular file only — hashing the bytes as
// they are written, and returns their sha256 (lowercase hex).
func copyNoFollow(root, rel, dst string) (string, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("bad path %q", rel)
		}
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	for _, p := range parts[:len(parts)-1] {
		next, err := openDirAt(fd, p)
		unix.Close(fd)
		if err != nil {
			return "", fmt.Errorf("%s: %w (a symlink is never followed)", p, err)
		}
		fd = next
	}
	ffd, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	unix.Close(fd)
	if err != nil {
		return "", fmt.Errorf("%w (a symlink is never followed)", err)
	}
	src := os.NewFile(uintptr(ffd), rel)
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), src); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ownPrivateDir checks dir is a real directory owned by this uid and closed
// to everyone else (a shared /tmp: someone else may have made it first).
func ownPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is not a private directory of this user", dir)
	}
	return nil
}

// layerHas reports whether a copy-on-write upper dir holds rel — a file the
// confined run wrote, or a whiteout (the run deleted it; copying it then
// fails as not a regular file, as it should).
func layerHas(upper, rel string) bool {
	_, err := os.Lstat(filepath.Join(upper, rel))
	return err == nil
}
