package repocache

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// safeTarget checks that target, under base (the home or the workspace),
// can take a cache: every existing component below base is a real directory
// (never a symlink — a workspace is the agent's, and a link there could
// send a mount or a copy anywhere), and target itself is absent or an empty
// directory. It returns whether target exists.
func safeTarget(base, target string) (bool, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false, fmt.Errorf("%s is not inside %s", target, base)
	}
	cur := base
	parts := strings.Split(rel, string(filepath.Separator))
	for i, c := range parts {
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !fi.IsDir() { // a symlink included: Lstat never follows one
			return false, fmt.Errorf("%s is a symlink or not a directory", cur)
		}
		if i == len(parts)-1 {
			f, err := os.Open(cur)
			if err != nil {
				return true, err
			}
			names, _ := f.Readdirnames(1)
			f.Close()
			if len(names) > 0 {
				return true, fmt.Errorf("%s: %w", cur, errNotEmpty)
			}
			return true, nil
		}
	}
	return false, nil
}

// mkdirUnder creates target's missing directories below base, refusing a
// symlink on the way.
func mkdirUnder(base, target string) error {
	rel, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is not inside %s", target, base)
	}
	cur := base
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, c)
		if err := os.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(cur)
		if err != nil {
			return err
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a directory", cur)
		}
	}
	return nil
}

// realDir makes p a plain directory of conductor's: anything else there (a
// symlink, a file) is removed first.
func realDir(p string) error {
	if fi, err := os.Lstat(p); err == nil {
		if fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		if err := RemoveAll(p); err != nil {
			return err
		}
	}
	return os.Mkdir(p, 0o700)
}

// copyTree copies src into dst (absent), never following a symlink: each
// regular file is cloned where the filesystem can (reflink), else copied.
// It stops past max bytes copied (0 = no cap) — clones are not counted.
func copyTree(src, dst string, max int64) error {
	var copied int64
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.Mkdir(t, fi.Mode().Perm()|0o700)
		case d.Type()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, t)
		case d.Type().IsRegular():
			cloned, err := cloneFile(p, t, fi.Mode().Perm())
			if err != nil {
				return err
			}
			if cloned {
				return nil
			}
			if copied += fi.Size(); max > 0 && copied > max {
				return errCopyCap
			}
			return copyFile(p, t, fi.Mode().Perm())
		}
		return nil // devices, sockets, fifos: never cached
	})
}

var errNotEmpty = errors.New("not empty")

var errCopyCap = errors.New("cache too large to copy here (no overlay or copy-on-write clone)")

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// moveTree moves src to dst: a rename, or a copy and a removal across
// filesystems.
func moveTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst, 0); err != nil {
		_ = RemoveAll(dst)
		return err
	}
	return RemoveAll(src)
}
