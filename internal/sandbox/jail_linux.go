//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	var libDirs []string
	if minimal {
		baseDirs = append([]string{"/usr"}, MinimalEtc...)
		libDirs = libraryDirs()
	}
	for _, d := range baseDirs {
		if err := bindInto(root, BindMount{Path: d, RO: true}, true); err != nil {
			return err
		}
	}
	if minimal {
		if err := noexecSystem(root, "/usr", libDirs); err != nil {
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
		if fi, err := os.Lstat(l); minimal && err == nil && fi.IsDir() {
			if err := noexecSystem(root, l, libDirs); err != nil {
				return err
			}
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
	var isDir bool
	var err error
	srcFd := -1
	if b.NoSymlink {
		// A declared path was checked when it was granted, with no symlink
		// left in it. It is opened without following one in any component
		// and bound by that open file, so one made since (by another
		// plugin that may write a directory above it) is refused, never
		// followed.
		srcFd, err = openNoSymlinks(src)
		if err == nil {
			defer unix.Close(srcFd)
			var st unix.Stat_t
			if err = unix.Fstat(srcFd, &st); err == nil {
				isDir = st.Mode&unix.S_IFMT == unix.S_IFDIR
			}
		} else if err == unix.ELOOP {
			return fmt.Errorf("jail bind %s: it goes through a symlink now", src)
		} else {
			err = &os.PathError{Op: "open", Path: src, Err: err}
		}
	} else {
		var fi os.FileInfo
		if fi, err = os.Stat(src); err == nil { // Stat: follow symlinks to learn dir-vs-file.
			isDir = fi.IsDir()
		}
	}
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
	if isDir {
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
	if srcFd >= 0 {
		err = bindFd(srcFd, target)
	} else {
		err = unix.Mount(src, target, "", unix.MS_BIND|unix.MS_REC, "")
	}
	if err != nil {
		return fmt.Errorf("jail bind %s: %w", src, err)
	}
	// Read-only and noexec all the way down: the submounts under it too.
	var set uint64
	if b.RO {
		set |= unix.MOUNT_ATTR_RDONLY
	}
	if b.NoExec {
		set |= unix.MOUNT_ATTR_NOEXEC | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV
	}
	if set != 0 {
		if err := setattr(target, set, true); err != nil {
			return fmt.Errorf("jail bind attributes %s: %w", b.Path, err)
		}
	}
	return nil
}

// openNoSymlinks opens p (O_PATH) without following a symlink in any of
// its components: openat2 RESOLVE_NO_SYMLINKS, or on a kernel without it
// (before 5.6) one component at a time with O_NOFOLLOW. A symlink is ELOOP.
func openNoSymlinks(p string) (int, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, p, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != unix.ENOSYS {
		return fd, err
	}
	if !filepath.IsAbs(p) {
		return -1, unix.EINVAL
	}
	cur, err := unix.Open("/", unix.O_PATH|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, err
	}
	for _, name := range strings.Split(strings.Trim(filepath.Clean(p), "/"), "/") {
		if name == "" {
			continue
		}
		next, err := unix.Openat(cur, name, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(cur)
		if err != nil {
			return -1, err
		}
		var st unix.Stat_t
		if err := unix.Fstat(next, &st); err != nil {
			unix.Close(next)
			return -1, err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			unix.Close(next)
			return -1, unix.ELOOP
		}
		cur = next
	}
	return cur, nil
}

// bindFd binds the file open at fd onto target: a clone of its mount tree
// (open_tree), moved into place, or on a kernel without that (before 5.2) a
// bind of its /proc/self/fd link, which names the open file itself. Neither
// resolves a path again. With neither, the bind fails: the launch fails
// rather than bind by path.
func bindFd(fd int, target string) error {
	tree, err := unix.OpenTree(fd, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH|unix.AT_RECURSIVE)
	if err == nil {
		defer unix.Close(tree)
		return unix.MoveMount(tree, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH)
	}
	if err != unix.ENOSYS {
		return fmt.Errorf("open_tree: %w", err)
	}
	if err := unix.Mount(fmt.Sprintf("/proc/self/fd/%d", fd), target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind by fd (no open_tree on this kernel, and /proc/self/fd: %w) — refusing to bind by path", err)
	}
	return nil
}

// setattr adds mount attributes (unix.MOUNT_ATTR_*) to the mount at p and,
// with rec, to every mount under it (mount_setattr AT_RECURSIVE; a remount,
// even with MS_REC, changes only the top one). A kernel without
// mount_setattr (before 5.12) gets a remount of p alone.
func setattr(p string, set uint64, rec bool) error {
	flags := uint(unix.AT_SYMLINK_NOFOLLOW)
	if rec {
		flags |= unix.AT_RECURSIVE
	}
	err := unix.MountSetattr(unix.AT_FDCWD, p, flags, &unix.MountAttr{Attr_set: set})
	if err != unix.ENOSYS {
		return err
	}
	f := uintptr(unix.MS_BIND|unix.MS_REMOUNT) | lockedFlags(p)
	for _, m := range []struct {
		attr uint64
		ms   uintptr
	}{
		{unix.MOUNT_ATTR_RDONLY, unix.MS_RDONLY}, {unix.MOUNT_ATTR_NOSUID, unix.MS_NOSUID},
		{unix.MOUNT_ATTR_NODEV, unix.MS_NODEV}, {unix.MOUNT_ATTR_NOEXEC, unix.MS_NOEXEC},
	} {
		if set&m.attr != 0 {
			f |= m.ms
		}
	}
	var st unix.Statfs_t
	if unix.Statfs(p, &st) == nil && int64(st.Flags)&unix.ST_RDONLY != 0 {
		f |= unix.MS_RDONLY
	}
	return unix.Mount("", p, "", f, "")
}

// noexecSystem makes the base-system tree dir (bound read-only at root+dir)
// noexec, then lets the dynamic linker's library directories under it
// (libDirs) map and run what is directly in them: shared libraries and the
// loader itself. Inside a library directory, a subdirectory that is not
// itself one (a tool's helpers, a daemon's binaries) and a program that is
// not a shared object stay noexec. What a plugin runs beyond that, its
// declared commands and exec paths, the caller binds in.
func noexecSystem(root, dir string, libDirs []string) error {
	if err := setattr(root+dir, unix.MOUNT_ATTR_NOEXEC|unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV, true); err != nil {
		return fmt.Errorf("jail noexec %s: %w", dir, err)
	}
	isLib := map[string]bool{}
	for _, l := range libDirs {
		isLib[l] = true
	}
	for _, l := range libDirs {
		if !pathWithin(dir, l) {
			continue
		}
		if err := bindSystem(root, l, false); err != nil {
			return err
		}
		ents, err := os.ReadDir(l)
		if err != nil {
			return fmt.Errorf("jail read %s: %w", l, err)
		}
		for _, e := range ents {
			p := filepath.Join(l, e.Name())
			switch {
			case e.IsDir():
				// glibc looks in glibc-hwcaps/<level>/ for optimised builds
				// of the same libraries.
				if isLib[p] || e.Name() == "glibc-hwcaps" {
					continue
				}
			case e.Type().IsRegular():
				fi, err := e.Info()
				if err != nil || fi.Mode()&0o111 == 0 || sharedObjectName(e.Name()) {
					continue
				}
			default:
				continue
			}
			if err := bindSystem(root, p, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// bindSystem binds host p at root+p (no submounts) read-only, nosuid and
// nodev, and noexec when asked.
func bindSystem(root, p string, noexec bool) error {
	if err := unix.Mount(p, root+p, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("jail bind %s: %w", p, err)
	}
	attr := uint64(unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV)
	if noexec {
		attr |= unix.MOUNT_ATTR_NOEXEC
	}
	if err := setattr(root+p, attr, false); err != nil {
		return fmt.Errorf("jail bind %s: %w", p, err)
	}
	return nil
}

// sharedObjectName: "libz.so", "libc.so.6", "ld-linux-x86-64.so.2".
func sharedObjectName(n string) bool {
	return strings.HasSuffix(n, ".so") || strings.Contains(n, ".so.")
}

func pathWithin(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// libraryDirs are the directories the dynamic linker loads libraries from on
// this machine, symlinks resolved: the standard ones, those /etc/ld.so.conf
// (and what it includes) lists for glibc, and musl's ld-musl-*.path.
func libraryDirs() []string {
	dirs := []string{"/lib", "/lib64", "/lib32", "/libx32", "/usr/lib", "/usr/lib64", "/usr/lib32", "/usr/libx32", "/usr/local/lib"}
	dirs = append(dirs, ldSoConf("/etc/ld.so.conf", 0)...)
	if paths, _ := filepath.Glob("/etc/ld-musl-*.path"); len(paths) > 0 {
		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil {
				dirs = append(dirs, strings.FieldsFunc(string(b), func(r rune) bool { return r == ':' || r == '\n' || r == ' ' || r == '\t' })...)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		real, err := filepath.EvalSymlinks(d)
		if err != nil || seen[real] {
			continue
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	// Shallowest first: a library directory inside another is bound after
	// (on top of) its parent's noexec subdirectories.
	sort.Slice(out, func(i, j int) bool {
		if di, dj := strings.Count(out[i], "/"), strings.Count(out[j], "/"); di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

// ldSoConf reads the directories a glibc ld.so.conf lists, following its
// include lines.
func ldSoConf(path string, depth int) []string {
	b, err := os.ReadFile(path)
	if err != nil || depth > 4 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if inc, ok := strings.CutPrefix(line, "include"); ok && (strings.HasPrefix(inc, " ") || strings.HasPrefix(inc, "\t")) {
			pat := strings.TrimSpace(inc)
			if !filepath.IsAbs(pat) {
				pat = filepath.Join(filepath.Dir(path), pat)
			}
			matches, _ := filepath.Glob(pat)
			sort.Strings(matches)
			for _, m := range matches {
				out = append(out, ldSoConf(m, depth+1)...)
			}
			continue
		}
		// "dir" or the old "dir=TYPE" form.
		d, _, _ := strings.Cut(strings.Fields(line)[0], "=")
		out = append(out, d)
	}
	return out
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
