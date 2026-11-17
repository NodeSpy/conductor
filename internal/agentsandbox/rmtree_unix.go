//go:build linux || darwin

package agentsandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// maxRemoveDepth bounds how deep removeTree goes: a deeper tree is refused
// (reported, kept aside), never walked with one open directory per level
// past it.
const maxRemoveDepth = 256

var (
	// errChanged: the entry is no longer the directory stat'ed (a swap).
	errChanged = errors.New("changed while being removed")
	// errMount: a mount point is never entered (its contents are not the
	// home's).
	errMount = errors.New("a mount point, not removed")
	// errTooDeep: past maxRemoveDepth.
	errTooDeep = fmt.Errorf("deeper than %d levels, not removed", maxRemoveDepth)
)

func sameInode(a, b *unix.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }

// removeTree removes path and everything under it, and returns the disk
// space it held. path is an agent's home: agent-controlled, so hostile.
// Every step is relative to an open directory fd and never follows a
// symlink — a link (symbolic or hard) is unlinked, never its target, and
// nothing but a directory checked by its fd is ever chmodded. It never
// enters a mount point, on any filesystem; a btrfs subvolume (its own
// st_dev, no mount) is the home's and goes. A directory the agent made
// read-only (a Go module cache: 0555 dirs, 0444 files) is made writable by
// its owner, on its own fd, before its entries go. The walk is iterative
// and bounded (maxRemoveDepth); it carries on past an entry it cannot
// remove and returns the first error.
func removeTree(path string) (int64, error) {
	path = filepath.Clean(path)
	pfd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(pfd)
	var pst unix.Stat_t
	if err := unix.Fstat(pfd, &pst); err != nil {
		return 0, err
	}
	w := &rmWalk{top: pfd, topDev: uint64(pst.Dev), at: path}
	w.push(pfd, filepath.Base(path), uint64(pst.Dev))
	if errors.Is(w.first, unix.ENOENT) && w.size == 0 {
		return 0, nil
	}
	w.run()
	return w.size, w.first
}

// rmFrame is one open directory on the walk: what is left of it.
type rmFrame struct {
	fd    int
	name  string // its name in its parent
	dev   uint64
	names []string
}

type rmWalk struct {
	top    int
	topDev uint64
	at     string // the tree's path, for messages only
	stack  []rmFrame
	size   int64
	first  error
}

// where is the path of entry n under the current stack (messages only).
func (w *rmWalk) where(n string) string {
	parts := []string{w.at}
	for _, f := range w.stack[min(1, len(w.stack)):] {
		parts = append(parts, f.name)
	}
	if len(w.stack) > 0 {
		parts = append(parts, n)
	}
	return strings.Join(parts, string(filepath.Separator))
}

func (w *rmWalk) fail(n string, err error) {
	if w.first == nil {
		w.first = fmt.Errorf("%s: %w", w.where(n), err)
	}
}

// push removes the non-directory n in dirfd, or opens the directory n and
// puts it on the stack. parentDev is the st_dev of the directory n is in.
func (w *rmWalk) push(dirfd int, n string, parentDev uint64) {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if !errors.Is(err, unix.ENOENT) || len(w.stack) == 0 {
			w.fail(n, err)
		}
		return
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		// Unlinked by name, never opened or chmodded: a symlink or a hard
		// link to a file outside the home loses only this name.
		if err := unix.Unlinkat(dirfd, n, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			w.fail(n, err)
			return
		}
		w.size += int64(st.Blocks) * 512
		return
	}
	if len(w.stack) >= maxRemoveDepth {
		w.fail(n, errTooDeep)
		return
	}
	// Every directory is checked, not only one on another st_dev: a bind
	// mount of the same filesystem has the home's st_dev.
	if mount, known := isMountRoot(dirfd, n); mount || (!known && uint64(st.Dev) != parentDev) {
		w.fail(n, errMount)
		return
	}
	fd, err := openDirForRemoval(dirfd, n, &st)
	if err != nil {
		w.fail(n, err)
		return
	}
	names, err := readNames(fd)
	if err != nil {
		w.fail(n, err)
	}
	w.size += int64(st.Blocks) * 512
	w.stack = append(w.stack, rmFrame{fd: fd, name: n, dev: uint64(st.Dev), names: names})
}

// run empties and removes the stacked directories, deepest first.
func (w *rmWalk) run() {
	for len(w.stack) > 0 {
		f := &w.stack[len(w.stack)-1]
		if k := len(f.names); k > 0 {
			n := f.names[k-1]
			f.names = f.names[:k-1]
			w.push(f.fd, n, f.dev)
			continue
		}
		done := *f
		unix.Close(done.fd)
		w.stack = w.stack[:len(w.stack)-1]
		parent := w.top
		if len(w.stack) > 0 {
			parent = w.stack[len(w.stack)-1].fd
		}
		if err := unix.Unlinkat(parent, done.name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
			w.fail(done.name, err)
		}
	}
}

// readNames lists the open directory fd's entries (fd stays open).
func readNames(fd int) ([]string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	d := os.NewFile(uintptr(dup), "")
	defer d.Close()
	return d.Readdirnames(-1)
}
