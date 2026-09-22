//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// buildJail builds a bwrap-style pivot_root ALLOW-LIST filesystem jail inside
// the current (unshared, private) mount namespace and pivots into it. Only the
// base system (interpreter essentials, read-only), a fresh /proc + minimal /dev
// + fresh /tmp, and the caller's allow-list `binds` are visible afterwards —
// everything else on the host, the daemon's state/config/secrets included, is
// gone by ABSENCE. The old root is detached, so there is nothing to umount back
// to.
//
// It runs as PID 1 inside the pid+mount namespace, holding CAP_SYS_ADMIN over
// the mount ns (the `unshare --map-root-user` maps the daemon uid to root-in-
// userns — mapping to a non-root uid clears effective caps on execve and makes
// every mount EPERM, which is what defeated the old overmount mask). The
// caller (RunEnter) drops that privilege via a nested user namespace before
// exec'ing the untrusted payload, so the payload cannot pivot back out.
func buildJail(binds []BindMount) error {
	root, err := os.MkdirTemp("", "conductor-jail-")
	if err != nil {
		return fmt.Errorf("jail root tmpdir: %w", err)
	}
	// pivot_root requires the new root to be a mount point; a fresh tmpfs is
	// the simplest one that also gives us a clean, writable skeleton to build
	// the mount targets in.
	if err := unix.Mount("tmpfs", root, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("tmpfs root: %w", err)
	}

	// Base system: read-only interpreter/shell essentials. Missing entries are
	// skipped so the jail works across distro layouts.
	for _, d := range []string{"/usr", "/etc"} {
		if err := bindInto(root, BindMount{Path: d, RO: true}, true); err != nil {
			return err
		}
	}
	// Merged-usr compatibility: on modern distros /bin,/lib,… are symlinks into
	// /usr (recreate the symlink); on older split-usr distros they are real
	// directories (bind them read-only).
	for _, l := range []string{"/bin", "/sbin", "/lib", "/lib64", "/libexec"} {
		if err := baseLink(root, l); err != nil {
			return err
		}
	}

	if err := mountProc(root); err != nil {
		return err
	}
	if err := mountDev(root); err != nil {
		return err
	}
	// A private /tmp: hides other processes' scratch (and other steps'
	// conductor-* temp dirs) while giving the payload a writable /tmp.
	if err := mountTmpfs(root+"/tmp", "mode=1777"); err != nil {
		return fmt.Errorf("jail /tmp: %w", err)
	}

	// The caller's allow-list: workdir, the code/ctx/egress temp dirs, and any
	// declared fs: paths. These MUST exist (fail closed, skipMissing=false).
	for _, b := range binds {
		if err := bindInto(root, b, false); err != nil {
			return err
		}
	}

	return pivotInto(root)
}

// bindInto binds b.Path into root at the same path. skipMissing tolerates a
// non-existent source (base-system entries that a given distro lacks); for the
// caller's declared allow-list it is false, so a missing path fails the launch.
func bindInto(root string, b BindMount, skipMissing bool) error {
	fi, err := os.Stat(b.Path) // Stat: follow symlinks to learn dir-vs-file.
	if err != nil {
		if os.IsNotExist(err) && skipMissing {
			return nil
		}
		return fmt.Errorf("jail bind %s: %w", b.Path, err)
	}
	target := root + b.Path
	if fi.IsDir() {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("jail mkdir %s: %w", target, err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("jail mkdir %s: %w", filepath.Dir(target), err)
		}
		if err := touchFile(target); err != nil {
			return fmt.Errorf("jail touch %s: %w", target, err)
		}
	}
	if err := unix.Mount(b.Path, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("jail bind %s: %w", b.Path, err)
	}
	if b.RO {
		if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("jail bind-ro %s: %w", b.Path, err)
		}
	}
	return nil
}

// baseLink recreates a merged-usr symlink (or binds a real split-usr dir
// read-only). A missing entry is silently skipped.
func baseLink(root, p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("jail lstat %s: %w", p, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		tgt, err := os.Readlink(p)
		if err != nil {
			return fmt.Errorf("jail readlink %s: %w", p, err)
		}
		if err := os.Symlink(tgt, root+p); err != nil && !os.IsExist(err) {
			return fmt.Errorf("jail symlink %s: %w", p, err)
		}
		return nil
	}
	return bindInto(root, BindMount{Path: p, RO: true}, true)
}

// mountProc mounts a fresh procfs for this pid namespace inside the jail (the
// --mount-proc one is on the old root, which pivot detaches).
func mountProc(root string) error {
	p := root + "/proc"
	if err := os.MkdirAll(p, 0o555); err != nil {
		return fmt.Errorf("jail mkdir /proc: %w", err)
	}
	if err := unix.Mount("proc", p, "proc", 0, ""); err != nil {
		return fmt.Errorf("jail /proc: %w", err)
	}
	return nil
}

// mountDev builds a minimal /dev: a tmpfs with the standard character devices
// bound in, the /dev/{fd,std*} → /proc/self/fd symlinks shells expect, and a
// /dev/shm tmpfs.
func mountDev(root string) error {
	dev := root + "/dev"
	if err := mountTmpfs(dev, "mode=0755"); err != nil {
		return fmt.Errorf("jail /dev: %w", err)
	}
	for _, n := range []string{"null", "zero", "full", "random", "urandom", "tty"} {
		src := "/dev/" + n
		if _, err := os.Stat(src); err != nil {
			continue
		}
		tgt := dev + "/" + n
		if err := touchFile(tgt); err != nil {
			return fmt.Errorf("jail touch /dev/%s: %w", n, err)
		}
		if err := unix.Mount(src, tgt, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("jail bind /dev/%s: %w", n, err)
		}
	}
	for _, l := range []struct{ name, tgt string }{
		{"fd", "/proc/self/fd"},
		{"stdin", "/proc/self/fd/0"},
		{"stdout", "/proc/self/fd/1"},
		{"stderr", "/proc/self/fd/2"},
	} {
		_ = os.Symlink(l.tgt, dev+"/"+l.name) // best-effort; absent /dev/fd is non-fatal.
	}
	if err := mountTmpfs(dev+"/shm", "mode=1777"); err != nil {
		return fmt.Errorf("jail /dev/shm: %w", err)
	}
	return nil
}

// mountTmpfs mkdirs path and mounts a tmpfs there.
func mountTmpfs(path, data string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return unix.Mount("tmpfs", path, "tmpfs", 0, data)
}

// pivotInto pivot_roots into root and lazily detaches the old root, leaving the
// jail as the only reachable filesystem.
func pivotInto(root string) error {
	old := root + "/.oldroot"
	if err := os.MkdirAll(old, 0o755); err != nil {
		return fmt.Errorf("jail mkdir oldroot: %w", err)
	}
	if err := unix.PivotRoot(root, old); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir new root: %w", err)
	}
	if err := unix.Unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	return os.Remove("/.oldroot")
}

// touchFile creates an empty file (a bind target for a device node / file).
func touchFile(p string) error {
	f, err := os.OpenFile(p, os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
