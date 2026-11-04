//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
func buildJail(binds []BindMount, noExecBase bool) error {
	root, err := os.MkdirTemp("", "conductor-jail-")
	if err != nil {
		return fmt.Errorf("jail root tmpdir: %w", err)
	}
	// pivot_root requires the new root to be a mount point; a fresh tmpfs is
	// the simplest one that also gives us a clean, writable skeleton to build
	// the mount targets in.
	// In a NoExecBase jail every writable mount is noexec, the root
	// included (the parents of the mount points are created on it).
	var wflags uintptr
	if noExecBase {
		wflags = unix.MS_NOEXEC | unix.MS_NOSUID | unix.MS_NODEV
	}
	if err := unix.Mount("tmpfs", root, "tmpfs", wflags, "mode=0755"); err != nil {
		return fmt.Errorf("tmpfs root: %w", err)
	}

	// Base system: read-only interpreter/shell essentials. Missing entries are
	// skipped so the jail works across distro layouts. /opt carries vendor
	// installs a /usr/bin wrapper execs (claude-code's /usr/bin/claude →
	// /opt/claude-code/bin/claude).
	for _, d := range []string{"/usr", "/etc", "/opt"} {
		if err := bindInto(root, BindMount{Path: d, RO: true}, true, false); err != nil {
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

	if noExecBase {
		// The program directories of the base system turn non-executable:
		// a payload confined to a declared command list cannot run an
		// undeclared tool by its absolute path either. Library directories
		// stay executable (shared objects are mapped executable). The
		// caller's binds land on top afterwards, so a declared command bound
		// at its own path inside one of these is executable again.
		for _, d := range noExecDirs {
			if err := remountNoExec(root, d); err != nil {
				return err
			}
		}
	}

	if err := mountProc(root); err != nil {
		return err
	}
	if err := mountDev(root, wflags); err != nil {
		return err
	}
	// A private /tmp: hides other processes' scratch (and other steps'
	// conductor-* temp dirs) while giving the payload a writable /tmp.
	if err := mountTmpfs(root+"/tmp", wflags, "mode=1777"); err != nil {
		return fmt.Errorf("jail /tmp: %w", err)
	}

	// The caller's allow-list: workdir, the code/ctx/egress temp dirs, and any
	// declared fs: paths. These MUST exist (fail closed, skipMissing=false)
	// unless marked Optional. Applied parent-first, so a tmpfs $HOME is in
	// place before the tool-state dirs inside it are bound, and a read-only
	// .git/config lands on top of the read-write common dir.
	for _, b := range orderBinds(binds) {
		var err error
		switch {
		case b.Tmpfs:
			err = mountTmpfs(root+b.Path, wflags, "mode=0700")
		case b.Link != "":
			if err = os.MkdirAll(filepath.Dir(root+b.Path), 0o755); err == nil {
				if err = os.Symlink(b.Link, root+b.Path); os.IsExist(err) {
					err = nil
				}
			}
		default:
			err = bindInto(root, b, b.Optional, noExecBase && !b.Exec)
		}
		if err != nil {
			return fmt.Errorf("jail %s: %w", b.Path, err)
		}
	}

	return pivotInto(root)
}

// noExecDirs are the base system's program directories (NoExecBase). Under
// merged /usr, /bin and /sbin are links into /usr and are covered by it.
var noExecDirs = []string{"/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/local/sbin", "/usr/games", "/bin", "/sbin", "/opt"}

// remountNoExec makes root+d a noexec mount of itself. A missing directory,
// or one that is a symlink in the jail (merged /usr), is skipped.
func remountNoExec(root, d string) error {
	target := root + d
	fi, err := os.Lstat(target)
	if err != nil || !fi.IsDir() {
		return nil
	}
	if err := unix.Mount(target, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("jail noexec bind %s: %w", d, err)
	}
	if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOEXEC|lockedFlags(target), ""); err != nil {
		return fmt.Errorf("jail noexec %s: %w", d, err)
	}
	return nil
}

// bindInto binds b.Path into root at the same path. skipMissing tolerates a
// non-existent source (base-system entries that a given distro lacks); for the
// caller's declared allow-list it is false, so a missing path fails the launch.
// noexec mounts it noexec, nosuid and nodev. Read-only and noexec apply to
// the bind and every mount under it (a recursive bind brings submounts).
func bindInto(root string, b BindMount, skipMissing, noexec bool) error {
	src := b.source()
	fi, err := os.Stat(src) // Stat: follow symlinks to learn dir-vs-file.
	if err != nil {
		if os.IsNotExist(err) && skipMissing {
			return nil
		}
		return fmt.Errorf("jail bind %s: %w", src, err)
	}
	target := root + b.Path
	if lfi, lerr := os.Lstat(target); lerr == nil && lfi.Mode()&os.ModeSymlink != 0 {
		// Never mount through a symlink already in the jail tree (a merged-usr
		// /bin link): the bind would land wherever it points.
		return fmt.Errorf("jail bind %s: target is a symlink", b.Path)
	}
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
	if err := unix.Mount(src, target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("jail bind %s: %w", src, err)
	}
	var flags uintptr
	if b.RO {
		flags |= unix.MS_RDONLY
	}
	if noexec {
		flags |= unix.MS_NOEXEC | unix.MS_NOSUID | unix.MS_NODEV
	}
	if flags == 0 {
		return nil
	}
	// A remount applies to one mount, MS_REC or not: each submount the
	// recursive bind brought along is remounted too.
	for _, m := range mountsUnder(target) {
		if err := unix.Mount("", m, "", unix.MS_BIND|unix.MS_REMOUNT|flags|lockedFlags(m), ""); err != nil {
			return fmt.Errorf("jail remount %s (%s): %w", b.Path, m, err)
		}
	}
	return nil
}

// mountsUnder lists target and every mount point below it, parents first,
// from /proc/self/mountinfo. target itself is always first, even if
// mountinfo cannot be read.
func mountsUnder(target string) []string {
	out := []string{target}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return out
	}
	seen := map[string]bool{target: true}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		mp := unescapeMountinfo(f[4])
		if !seen[mp] && strings.HasPrefix(mp, target+"/") {
			seen[mp] = true
			out = append(out, mp)
		}
	}
	return out
}

// unescapeMountinfo decodes mountinfo's octal escapes (\040 for a space).
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// lockedFlags are the source mount's nosuid/nodev/noexec/relatime flags. In
// a user namespace they are LOCKED on an inherited mount: a read-only
// remount that does not repeat them is refused with EPERM (a container's
// tmpfs is typically nosuid,nodev,noexec).
func lockedFlags(p string) uintptr {
	var st unix.Statfs_t
	if unix.Statfs(p, &st) != nil {
		return 0
	}
	var f uintptr
	for _, m := range []struct {
		st int64
		ms uintptr
	}{
		{unix.ST_NOSUID, unix.MS_NOSUID}, {unix.ST_NODEV, unix.MS_NODEV}, {unix.ST_NOEXEC, unix.MS_NOEXEC},
		{unix.ST_NOATIME, unix.MS_NOATIME}, {unix.ST_NODIRATIME, unix.MS_NODIRATIME}, {unix.ST_RELATIME, unix.MS_RELATIME},
	} {
		if int64(st.Flags)&m.st != 0 {
			f |= m.ms
		}
	}
	return f
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
	return bindInto(root, BindMount{Path: p, RO: true}, true, false)
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
func mountDev(root string, flags uintptr) error {
	dev := root + "/dev"
	if err := mountTmpfs(dev, flags, "mode=0755"); err != nil {
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
	if err := mountTmpfs(dev+"/shm", flags, "mode=1777"); err != nil {
		return fmt.Errorf("jail /dev/shm: %w", err)
	}
	return nil
}

// mountTmpfs mkdirs path and mounts a tmpfs there with flags.
func mountTmpfs(path string, flags uintptr, data string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return unix.Mount("tmpfs", path, "tmpfs", flags, data)
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
	// Already there (an outer bind showed it): any non-directory inode is a
	// mount point as it is. Opening it is not needed, and would fail on a
	// socket (ENXIO) or block on a FIFO.
	if fi, err := os.Lstat(p); err == nil && !fi.IsDir() {
		return nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
