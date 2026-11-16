//go:build linux

package repocache

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// cloneFile clones src to dst (absent) with FICLONE where the filesystem
// shares extents (btrfs, xfs); false when it cannot, with nothing left.
func cloneFile(src, dst string, mode os.FileMode) (bool, error) {
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o200)
	if err != nil {
		return false, err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return false, nil
	}
	return true, out.Close()
}

// cloneDir clones a whole tree in one call where the platform can (macOS):
// never on Linux.
func cloneDir(src, dst string) bool { return false }

var overlayProbes sync.Map // "lowerRoot\x00upperRoot" → bool

// OverlayUsable reports whether an unprivileged overlay mount works inside
// a user namespace with its lower layer under lowerRoot and its upper
// layer under upperRoot — the agent jail's own setup (a root-mapped user
// and mount namespace). Probed once per pair: a kernel without unprivileged
// overlay (before 5.11), a seccomp policy that refuses it, or an upper
// filesystem overlay cannot write on (NFS) falls back to a copy.
func OverlayUsable(lowerRoot, upperRoot string) (bool, string) {
	k := lowerRoot + "\x00" + upperRoot
	if v, ok := overlayProbes.Load(k); ok {
		r := v.([2]string)
		return r[0] == "1", r[1]
	}
	ok, why := probeOverlay(lowerRoot, upperRoot)
	v := [2]string{"0", why}
	if ok {
		v[0] = "1"
	}
	overlayProbes.Store(k, v)
	return ok, why
}

func probeOverlay(lowerRoot, upperRoot string) (bool, string) {
	if _, err := exec.LookPath("unshare"); err != nil {
		return false, "unshare is not installed"
	}
	if _, err := exec.LookPath("mount"); err != nil {
		return false, "mount is not installed"
	}
	if err := os.MkdirAll(lowerRoot, 0o700); err != nil {
		return false, err.Error()
	}
	if err := os.MkdirAll(upperRoot, 0o700); err != nil {
		return false, err.Error()
	}
	lo, err := os.MkdirTemp(lowerRoot, ".overlay-probe-")
	if err != nil {
		return false, err.Error()
	}
	defer RemoveAll(lo)
	up, err := os.MkdirTemp(upperRoot, ".overlay-probe-")
	if err != nil {
		return false, err.Error()
	}
	defer RemoveAll(up)
	for _, d := range []string{filepath.Join(lo, "l"), filepath.Join(up, "u"), filepath.Join(up, "w"), filepath.Join(up, "m")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return false, err.Error()
		}
	}
	if err := os.WriteFile(filepath.Join(lo, "l", "f"), []byte("lower"), 0o400); err != nil {
		return false, err.Error()
	}
	for _, p := range []string{lo, up} {
		if strings.ContainsAny(p, ",:") {
			return false, p + " has a ',' or ':' an overlay mount cannot name"
		}
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", filepath.Join(lo, "l"), filepath.Join(up, "u"), filepath.Join(up, "w"))
	m := filepath.Join(up, "m")
	// Mount, write through, remove a lower file: the lower must be
	// untouched after.
	script := `{ mount -t overlay overlay -o "$1,volatile" "$2" 2>/dev/null || mount -t overlay overlay -o "$1" "$2"; } && echo x > "$2/new" && rm -f "$2/f" && test -f "$2/new" && test ! -e "$2/f"`
	out, err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "sh", "-c", script, "sh", opts, m).CombinedOutput()
	if err != nil {
		return false, strings.TrimSpace(fmt.Sprintf("%v: %s", err, out))
	}
	if b, err := os.ReadFile(filepath.Join(lo, "l", "f")); err != nil || string(b) != "lower" {
		return false, "the probe's lower layer changed"
	}
	if _, err := os.Lstat(filepath.Join(up, "u", "new")); err != nil {
		return false, "the probe's write did not reach its upper layer"
	}
	return true, ""
}
