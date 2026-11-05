package blob

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Untar unpacks a tar archive of a directory (an agent's workspace.from
// artifact) into dst, which must not exist yet. The archive is treated as
// untrusted whatever made it: every entry lands inside
// dst (writes go through an os.Root, so not even a symlink planted earlier in
// the archive can redirect one), a symlink must resolve inside dst, and
// anything but a directory, regular file or symlink — a hard link, a device —
// fails the unpack. On failure dst is removed.
func Untar(r io.Reader, dst string) (err error) {
	if err := os.Mkdir(dst, 0o700); err != nil {
		return fmt.Errorf("blob: unpack: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dst)
			err = fmt.Errorf("blob: unpack: %w", err)
		}
	}()
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	var links []string
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." && hdr.Typeflag == tar.TypeDir {
			continue // the archive's own root ("./"): dst is it
		}
		if name == "." || path.IsAbs(hdr.Name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("entry %q is outside the archive's directory", hdr.Name)
		}
		perm := fs.FileMode(hdr.Mode).Perm()
		if dir := path.Dir(name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, perm|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if !linkInside(dst, name, hdr.Linkname) {
				return fmt.Errorf("%s is a symlink to %s, outside the archive's directory", name, hdr.Linkname)
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
			links = append(links, name)
		default:
			return fmt.Errorf("%s: entry type %q is not a file, directory or symlink", name, hdr.Typeflag)
		}
	}
	// A link that pointed inside when it was made can be steered out by one
	// made after it (a -> b/.., then b -> .): check them all on the
	// finished tree.
	for _, l := range links {
		target, err := os.Readlink(filepath.Join(dst, filepath.FromSlash(l)))
		if err != nil {
			return err
		}
		if !linkInside(dst, l, target) {
			return fmt.Errorf("%s is a symlink to %s, which resolves outside the archive's directory", l, target)
		}
	}
	return nil
}

// linkInside reports whether the symlink at rel (slash-separated, under
// root) with the given target stays inside root when followed — resolved
// the way the kernel would, through whatever links the tree holds now.
func linkInside(root, rel, target string) bool {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) {
		return false
	}
	comps := strings.Split(path.Dir(rel), "/")
	comps = append(comps, strings.Split(filepath.ToSlash(target), "/")...)
	hops := 0
	return resolvesInside(root, comps, &hops)
}

// resolvesInside walks comps from root, expanding each symlink it meets,
// and reports whether the walk never climbs above root. A missing
// component is taken as a plain directory (the link may dangle).
func resolvesInside(root string, comps []string, hops *int) bool {
	var cur []string
	for i, c := range comps {
		switch c {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return false
			}
			cur = cur[:len(cur)-1]
			continue
		}
		next := append(cur[:len(cur):len(cur)], c)
		p := filepath.Join(append([]string{root}, next...)...)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if *hops++; *hops > 40 {
			return false // a loop
		}
		t, err := os.Readlink(p)
		if err != nil || t == "" || filepath.IsAbs(t) {
			return false
		}
		rest := append(append(append([]string{}, cur...), strings.Split(filepath.ToSlash(t), "/")...), comps[i+1:]...)
		return resolvesInside(root, rest, hops)
	}
	return true
}
