//go:build darwin

package repocache

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// On macOS a restore is an APFS clone: the run's copy shares the shared
// cache's blocks (almost no disk), the copy cap does not apply to it, and a
// run changing its copy never changes the shared one.
func TestRestoreClonesOnAPFS(t *testing.T) {
	h := newHarness(t, false)
	h.m.CopyMax = 1 << 20 // a clone is not a copy: the cap never applies
	const size = 64 << 20
	big := make([]byte, size)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	warm := h.m.Warm
	h.m.Warm = func(ctx context.Context, w WarmRun) error {
		if err := warm(ctx, w); err != nil {
			return err
		}
		if w.Cache == "go" {
			return os.WriteFile(filepath.Join(w.Home, "go", "pkg", "mod", "dep@v1", "big"), big, 0o444)
		}
		return nil
	}
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	if !cloneDir(h.m.Store.Dir, filepath.Join(t.TempDir(), "probe")) {
		t.Skip("this volume has no clonefile (not APFS)")
	}
	before := snapshot(t, h.m.Store.Dir)
	req, _ := h.run(t, "mac-run", map[string]string{"go.sum": "default-branch deps\n", "tool.lock": "v1\n"})
	free0 := freeBytes(t, req.Home)
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	free1 := freeBytes(t, req.Home)
	if len(g.Mounts) != 0 {
		t.Fatalf("overlay mounts on macOS: %+v", g.Mounts)
	}
	got := filepath.Join(req.Home, "go", "pkg", "mod", "dep@v1", "big")
	b, err := os.ReadFile(got)
	if err != nil || len(b) != size {
		t.Fatalf("restored %d bytes: %v", len(b), err)
	}
	if used := free0 - free1; used > size/4 {
		t.Errorf("the restore took %d bytes of disk for a %d-byte cache: copied, not cloned", used, size)
	}
	// The run changes its copy; the shared one is untouched.
	if err := os.Chmod(got, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(got, []byte("changed by the run"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(req.Home, "go", "pkg", "mod", "dep@v1", "dep.go")); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, h.m.Store.Dir)
	for p, v := range before {
		if filepath.Base(p) != ".used" && after[p] != v {
			t.Errorf("the run changed the shared cache at %s", p)
		}
	}
}

func freeBytes(t *testing.T, p string) int64 {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		t.Fatal(err)
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
