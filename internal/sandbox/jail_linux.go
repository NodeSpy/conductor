//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/NodeSpy/conductor/internal/scratch"
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
//
// The root is a tmpfs mounted on jailRoot, the same directory for every jail
// (the mount lives only in this private namespace, so jails never see each
// other's): the host is left no per-launch directory to clean up.
// jailRootDir is the directory the jail's root is mounted on: dir, else (or
// when dir can't be made, e.g. from inside another jail) one fixed directory
// under os.TempDir().
func jailRootDir(dir string) (string, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			return dir, nil
		}
	}
	dir = filepath.Join(os.TempDir(), "conductor-jail")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// jailTmp is the jail's /tmp: a directory of its own on disk, in tmpParent,
// held (scratch.Held) while the jail runs and removed after (release). The
// parent stays open across the pivot so release can reach the directory once
// the jail's view of it is detached.
type jailTmp struct {
	held   *scratch.Held
	parent *os.Root
}

func newJailTmp(tmpParent string) *jailTmp {
	if tmpParent == "" {
		return nil
	}
	h, err := scratch.NewHeld(tmpParent, "tmp-")
	if err != nil {
		return nil
	}
	r, err := os.OpenRoot(tmpParent)
	if err != nil {
		h.Remove()
		return nil
	}
	return &jailTmp{held: h, parent: r}
}

// release removes the jail's /tmp. /tmp is detached first, with every bind
// the caller stacked under it, so the removal never reaches into a bound
// host directory.
func (t *jailTmp) release() {
	if t == nil {
		return
	}
	_ = unix.Unmount("/tmp", unix.MNT_DETACH)
	_ = t.parent.RemoveAll(filepath.Base(t.held.Path))
	t.parent.Close()
	t.held.Remove() // drops the lock; the directory is already gone
}

// buildJail builds the jail (see above) and returns what to call once the
// payload has exited: it removes the jail's on-disk /tmp. tmpParent is where
// that /tmp is made ("" or unusable: a private tmpfs, as before).
func buildJail(binds []BindMount, jailRoot, tmpParent, base string) (func(), error) {
	t := newJailTmp(tmpParent)
	if err := buildJailIn(binds, jailRoot, t, base); err != nil {
		t.release()
		return nil, err
	}
	return t.release, nil
}

func buildJailIn(binds []BindMount, jailRoot string, t *jailTmp, base string) error {
	minimal := base == BaseMinimal
	if base != "" && !minimal {
		return fmt.Errorf("jail: unknown base %q", base)
	}
	root, err := jailRootDir(jailRoot)
	if err != nil {
		return fmt.Errorf("jail root: %w", err)
	}
	// pivot_root requires the new root to be a mount point; a fresh tmpfs is
	// the simplest one that also gives us a clean, writable skeleton to build
	// the mount targets in.
	if err := unix.Mount("tmpfs", root, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("tmpfs root: %w", err)
	}

	// Base system: read-only interpreter/shell essentials. Missing entries are
	// skipped so the jail works across distro layouts. /opt carries vendor
	// installs a /usr/bin wrapper execs (claude-code's /usr/bin/claude →
	// /opt/claude-code/bin/claude).
	//
	// The minimal base (a plugin's) shows /usr without its programs: the
	// standard PATH directories are empty tmpfs mounts the caller binds the
	// declared commands into, then made read-only. /etc is the MinimalEtc
	// list, and there is no /opt.
	baseDirs := []string{"/usr", "/etc", "/opt"}
	if minimal {
		baseDirs = append([]string{"/usr"}, MinimalEtc...)
	}
	for _, d := range baseDirs {
		if err := bindInto(root, BindMount{Path: d, RO: true}, true); err != nil {
			return err
		}
	}
	var binDirs []string
	if minimal {
		for _, d := range minimalBinDirs {
			if fi, err := os.Lstat(root + d); err != nil || !fi.IsDir() {
				continue
			}
			if err := mountTmpfs(root+d, "mode=0755"); err != nil {
				return fmt.Errorf("jail empty %s: %w", d, err)
			}
			binDirs = append(binDirs, d)
		}
	}
	// Merged-usr compatibility: on modern distros /bin,/lib,… are symlinks into
	// /usr (recreate the symlink); on older split-usr distros they are real
	// directories (bind them read-only; the minimal base leaves a split-usr
	// /bin and /sbin empty, like /usr/bin).
	for _, l := range []string{"/bin", "/sbin", "/lib", "/lib64", "/libexec"} {
		if minimal && (l == "/bin" || l == "/sbin") {
			if fi, err := os.Lstat(l); err == nil && fi.IsDir() {
				if err := mountTmpfs(root+l, "mode=0755"); err != nil {
					return fmt.Errorf("jail empty %s: %w", l, err)
				}
				binDirs = append(binDirs, l)
				continue
			}
		}
		if err := baseLink(root, l); err != nil {
			return err
		}
	}

	if err := mountProc(root); err != nil {
		return err
	}
	if err := mountDev(root, minimal); err != nil {
		return err
	}
	// A private /tmp: hides other processes' scratch (and other steps'
	// conductor-* temp dirs) while giving the payload a writable /tmp. On
	// disk in the scratch dir when there is one: a tmpfs is memory, and
	// what a payload writes to /tmp (a clone, a build) can be large.
	if t != nil {
		err = bindInto(root, BindMount{Path: "/tmp", Src: t.held.Path}, false)
	} else {
		err = mountTmpfs(root+"/tmp", "mode=1777")
	}
	if err == nil && minimal {
		err = remount(root+"/tmp", unix.MS_NOEXEC|unix.MS_NOSUID|unix.MS_NODEV)
	}
	if err != nil {
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
			err = mountTmpfs(root+b.Path, "mode=0700")
		case b.Link != "":
			if err = os.MkdirAll(filepath.Dir(root+b.Path), 0o755); err == nil {
				if err = os.Symlink(b.Link, root+b.Path); os.IsExist(err) {
					err = nil
				}
			}
		default:
			err = bindInto(root, b, b.Optional)
		}
		if err != nil {
			return fmt.Errorf("jail %s: %w", b.Path, err)
		}
		if b.NoExec && !b.Tmpfs && b.Link == "" {
			if err := remount(root+b.Path, unix.MS_NOEXEC|unix.MS_NOSUID|unix.MS_NODEV); err != nil && !(b.Optional && os.IsNotExist(err)) {
				return fmt.Errorf("jail noexec %s: %w", b.Path, err)
			}
		}
	}
	// The emptied PATH directories hold only what was bound into them, and
	// stay that way: read-only, so nothing can be added to run.
	for _, d := range binDirs {
		if err := unix.Mount("", root+d, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return fmt.Errorf("jail read-only %s: %w", d, err)
		}
	}

	if err := pivotInto(root); err != nil {
		return err
	}
	if minimal {
		// The root tmpfs itself (the skeleton the mounts hang on) is
		// read-only and noexec too: no file can be made there to run.
		if err := unix.Mount("", "/", "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOEXEC|unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
			return fmt.Errorf("jail read-only root: %w", err)
		}
	}
	return nil
}

// remount re-applies a bind mount at p with extra flags (noexec on a
// writable path), keeping the flags its source has locked.
func remount(p string, flags uintptr) error {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return err
	}
	f := uintptr(unix.MS_BIND|unix.MS_REMOUNT) | flags | lockedFlags(p)
	if int64(st.Flags)&unix.ST_RDONLY != 0 {
		f |= unix.MS_RDONLY
	}
	return unix.Mount("", p, "", f, "")
}

// bindInto binds b.Path into root at the same path. skipMissing tolerates a
// non-existent source (base-system entries that a given distro lacks); for the
// caller's declared allow-list it is false, so a missing path fails the launch.
func bindInto(root string, b BindMount, skipMissing bool) error {
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
	if b.RO {
		if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_REC|lockedFlags(src), ""); err != nil {
			return fmt.Errorf("jail bind-ro %s: %w", b.Path, err)
		}
	}
	return nil
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
func mountDev(root string, noexec bool) error {
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
	if noexec {
		// Nothing written under /dev runs: /dev/shm noexec, /dev itself
		// read-only once its nodes are in place.
		if err := unix.Mount("", dev+"/shm", "", unix.MS_REMOUNT|unix.MS_NOEXEC|unix.MS_NOSUID|unix.MS_NODEV, "mode=1777"); err != nil {
			return fmt.Errorf("jail /dev/shm noexec: %w", err)
		}
		if err := unix.Mount("", dev, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOEXEC|unix.MS_NOSUID, "mode=0755"); err != nil {
			return fmt.Errorf("jail /dev read-only: %w", err)
		}
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
