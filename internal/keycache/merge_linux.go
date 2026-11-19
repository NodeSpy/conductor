//go:build linux

package keycache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// mergeLayers builds at dst (absent) what a launch saw at an overlaid cache
// path: the published lower layer with the launch's upper layer applied, as
// overlayfs would show them. lower is a published version's (conductor's
// own, immutable): its files are hard-linked, never copied, and never
// written after. upper is the ended launch's (beside the store: no sandbox
// ever saw where it is, and its launch is gone): its files are moved in.
// What upper says is applied the overlay's way:
//
//   - a whiteout (a 0/0 character device, or a file marked
//     overlay.whiteout) removes the name;
//   - an opaque directory (overlay.opaque=y) replaces the lower's whole;
//   - anything else replaces the lower's entry of that name.
//
// A metacopy or redirect mark (an upper entry whose data or name is the
// lower's) refuses the merge: conductor's overlays never set one, so one
// is not this conductor's layer. budget bounds the bytes moved or copied in.
func mergeLayers(lower, upper, dst string, budget *int64) error {
	if lower != "" {
		if err := linkTree(lower, dst); err != nil {
			return err
		}
	} else if err := os.Mkdir(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(upper, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(upper, p)
		if rel == "." {
			return nil
		}
		t := filepath.Join(dst, rel)
		for _, x := range []string{"metacopy", "redirect"} {
			if hasOverlayXattr(p, x) {
				return fmt.Errorf("%s: an overlay %s mark (not this conductor's layer): not published", rel, x)
			}
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if whiteout(p, fi) {
			return removeIfAny(t)
		}
		switch {
		case d.IsDir():
			if overlayXattr(p, "opaque") == "y" {
				if err := removeIfAny(t); err != nil {
					return err
				}
			} else if cur, err := os.Lstat(t); err == nil && cur.IsDir() {
				return nil
			} else if err := removeIfAny(t); err != nil {
				return err
			}
			return os.Mkdir(t, fi.Mode().Perm()|0o700)
		case d.Type().IsRegular():
			if *budget -= allocated(fi); *budget < 0 {
				return errCopyCap
			}
			if err := removeIfAny(t); err != nil {
				return err
			}
			if err := os.Rename(p, t); err != nil {
				return copyFile(p, t, fi.Mode().Perm())
			}
			// What a published copy holds is plain files: no setuid,
			// setgid or sticky bit survives the move in (copyFromHome
			// masks the same way).
			return os.Chmod(t, fi.Mode().Perm())
		case d.Type()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := removeIfAny(t); err != nil {
				return err
			}
			return os.Symlink(l, t)
		}
		return nil // devices, sockets, fifos: never cached
	})
}

// linkTree recreates src (a published version's) at dst (absent): its
// directories made, its regular files hard-linked, its symlinks copied as
// links.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return os.Mkdir(t, fi.Mode().Perm()|0o700)
		case d.Type().IsRegular():
			return os.Link(p, t)
		case d.Type()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, t)
		}
		return nil
	})
}

// whiteout reports an overlay whiteout: a character device numbered 0/0,
// or (kernels with xattr whiteouts) an empty file marked overlay.whiteout.
func whiteout(p string, fi fs.FileInfo) bool {
	if fi.Mode()&fs.ModeCharDevice != 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Rdev == 0 {
			return true
		}
		return false
	}
	return fi.Mode().IsRegular() && fi.Size() == 0 && hasOverlayXattr(p, "whiteout")
}

// overlayXattr reads an overlay mark: the unprivileged (user.) one an
// overlay mounted in a user namespace writes, or the trusted. one.
func overlayXattr(p, name string) string {
	buf := make([]byte, 64)
	for _, ns := range []string{"user.overlay.", "trusted.overlay."} {
		if n, err := unix.Lgetxattr(p, ns+name, buf); err == nil {
			return string(buf[:n])
		}
	}
	return ""
}

func hasOverlayXattr(p, name string) bool {
	buf := make([]byte, 1)
	for _, ns := range []string{"user.overlay.", "trusted.overlay."} {
		if _, err := unix.Lgetxattr(p, ns+name, buf); err == nil || err == unix.ERANGE {
			return true
		}
	}
	return false
}
