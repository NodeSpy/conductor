//go:build darwin

package keycache

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// On macOS a publish clones what the run filled out of its home, and a
// restore is an APFS clone of the published copy: the next run's copy
// shares the store's blocks (almost no disk), the copy cap does not apply,
// and a run changing its copy never changes the store's.
func TestPublishAndRestoreCloneOnAPFS(t *testing.T) {
	h := newHarness(t, false)
	h.m.CopyMax = 1 << 20 // a clone is not a copy: the cap never applies
	if !cloneDir(h.m.Store.Dir, filepath.Join(t.TempDir(), "probe")) {
		t.Skip("this volume has no clonefile (not APFS)")
	}
	const size = 64 << 20
	blob := make([]byte, size)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	k := key("mac")
	req, g := h.launch(t, "d1", k)
	if len(g.Mounts) != 0 {
		t.Fatalf("overlay mounts on macOS: %+v", g.Mounts)
	}
	release, err := LockShared(g.Lock)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(req.Home, ".cache", "fake", "big")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("big", filepath.Join(req.Home, ".cache", "fake", "link")); err != nil {
		t.Fatal(err)
	}
	EndLaunch(g.Lock)
	release()
	p := h.finish(t, "d1", true)
	if p == nil {
		t.Fatalf("not published: %v", h.logs)
	}
	a, _ := os.Stat(src)
	b, _ := os.Stat(filepath.Join(p.Entry.Dir, "data", ".cache", "fake", "big"))
	if b == nil || os.SameFile(a, b) {
		t.Fatal("the published copy is missing or shares the home's inode")
	}
	if l, err := os.Readlink(filepath.Join(p.Entry.Dir, "data", ".cache", "fake", "link")); err != nil || l != "big" {
		t.Fatalf("the symlink: %q %v", l, err)
	}

	before := snapshot(t, h.m.Store.Dir)
	home2 := filepath.Join(h.homes, "second")
	if err := os.MkdirAll(home2, 0o700); err != nil {
		t.Fatal(err)
	}
	free0 := freeBytes(t, home2)
	h.restore(t, Request{Workspace: t.TempDir(), Home: home2, Run: "d2", Dispatch: "d2", Key: k})
	free1 := freeBytes(t, home2)
	got := filepath.Join(home2, ".cache", "fake", "big")
	data, err := os.ReadFile(got)
	if err != nil || len(data) != size {
		t.Fatalf("restored %d bytes: %v", len(data), err)
	}
	if used := free0 - free1; used > size/4 {
		t.Errorf("the restore took %d bytes of disk for a %d-byte cache: copied, not cloned", used, size)
	}
	if err := os.WriteFile(got, []byte("changed by the run"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, h.m.Store.Dir)
	for f, v := range before {
		if filepath.Base(f) != ".used" && after[f] != v {
			t.Errorf("the run changed the store at %s", f)
		}
	}
}

// snapshot is every regular file under dir → its content.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func freeBytes(t *testing.T, p string) int64 {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		t.Fatal(err)
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
