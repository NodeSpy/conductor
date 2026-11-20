package keycache

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// setWhiteout marks p an overlay whiteout the way an overlay mounted in a
// user namespace does (an empty file with user.overlay.whiteout).
func setWhiteout(p string) error {
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		return err
	}
	return unix.Lsetxattr(p, "user.overlay.whiteout", nil, 0)
}

// mergeLayers applies an upper layer over a lower one with overlay
// semantics: new and changed files win, a whiteout deletes, an opaque
// directory hides the lower one whole, and a metacopy or redirect (whose
// data lives elsewhere) is refused rather than published half.
func TestMergeLayersOverlaySemantics(t *testing.T) {
	d := t.TempDir()
	lower, upper, out := filepath.Join(d, "lower"), filepath.Join(d, "upper"), filepath.Join(d, "out")
	for _, f := range []string{"keep", "changed", "gone", "opaque/old", "dir/sub/a"} {
		write(t, filepath.Join(lower, f), "lower "+f)
	}
	write(t, filepath.Join(upper, "changed"), "upper")
	write(t, filepath.Join(upper, "new"), "upper new")
	write(t, filepath.Join(upper, "opaque", "fresh"), "fresh")
	if err := setWhiteout(filepath.Join(upper, "gone")); err != nil {
		t.Skipf("no user xattrs here: %v", err)
	}
	if err := unix.Lsetxattr(filepath.Join(upper, "opaque"), "user.overlay.opaque", []byte("y"), 0); err != nil {
		t.Fatal(err)
	}
	budget := int64(1 << 30)
	if err := mergeLayers(lower, upper, out, &budget); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"keep": "lower keep", "changed": "upper", "new": "upper new", "opaque/fresh": "fresh", "dir/sub/a": "lower dir/sub/a"} {
		if got := read(t, filepath.Join(out, f)); got != want {
			t.Errorf("%s = %q, want %q", f, got, want)
		}
	}
	for _, f := range []string{"gone", "opaque/old"} {
		if _, err := os.Lstat(filepath.Join(out, f)); err == nil {
			t.Errorf("%s survived the merge", f)
		}
	}
	// The merged copy never shares an inode with the upper layer (a run's
	// home may still write it).
	a, _ := os.Stat(filepath.Join(upper, "new"))
	b, _ := os.Stat(filepath.Join(out, "new"))
	if os.SameFile(a, b) {
		t.Fatal("the merge links the upper layer")
	}

	meta := filepath.Join(d, "meta")
	write(t, filepath.Join(meta, "f"), "")
	if err := unix.Lsetxattr(filepath.Join(meta, "f"), "user.overlay.metacopy", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := mergeLayers(lower, meta, filepath.Join(d, "out2"), &budget); err == nil {
		t.Fatal("merged a metacopy")
	}
}

// The kernel's own whiteout — what an overlay in a launch writes when its
// run deletes a file it was shown — is a 0/0 character device (creatable
// unprivileged since Linux 5.8).
func TestMergeLayersHonoursDeviceWhiteouts(t *testing.T) {
	d := t.TempDir()
	lower, upper, out := filepath.Join(d, "lower"), filepath.Join(d, "upper"), filepath.Join(d, "out")
	write(t, filepath.Join(lower, "gone"), "lower")
	write(t, filepath.Join(lower, "kept"), "lower")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(filepath.Join(upper, "gone"), unix.S_IFCHR|0o600, 0); err != nil {
		t.Skipf("no unprivileged whiteouts here: %v", err)
	}
	budget := int64(1 << 30)
	if err := mergeLayers(lower, upper, out, &budget); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(out, "gone")); err == nil {
		t.Fatal("a device whiteout did not delete")
	}
	if read(t, filepath.Join(out, "kept")) != "lower" {
		t.Fatal("kept")
	}
}
