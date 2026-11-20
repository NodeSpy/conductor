//go:build linux || darwin

package keycache

// Regression tests from the second adversarial review of the step caches:
// each asserts a property a finding showed broken.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const r2tok = "ghs_R2GIVENTOKEN1234567890abcdef"

// seedTool publishes k once so ~/.cache/tool is learned and the next
// launch of k gets it by overlay (or copy).
func seedTool(t *testing.T, h *harness, k Key) {
	t.Helper()
	req, g := h.launch(t, "seed", k)
	runWrites(t, req, g, map[string]string{".cache/tool/blob": big})
	if h.finish(t, "seed", true) == nil {
		t.Fatalf("seed not published\n%s", strings.Join(h.logs, "\n"))
	}
}

func upperOf(t *testing.T, req Request, g Grant, rel string) string {
	t.Helper()
	p := filepath.Join(req.Home, rel)
	for _, mt := range g.Mounts {
		if r, err := filepath.Rel(mt.Target, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.Join(mt.Upper, r)
		}
	}
	return p
}

// A sparse file is budgeted by
// its allocated blocks but copied with io.Copy, which writes every hole:
// the published version takes its full apparent size, past MaxSize.
func TestSparseFileIsChargedItsSize(t *testing.T) {
	h := newHarness(t, false)
	h.m.MaxSize = 64 << 20
	k := key("sparse")
	req, g := h.launch(t, "a", k)
	release, err := LockShared(g.Lock)
	if err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(req.Home, ".cache", "tool")
	write(t, filepath.Join(d, "blob"), big)
	f, err := os.Create(filepath.Join(d, "sparse.img"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(512 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	EndLaunch(g.Lock)
	release()
	p := h.finish(t, "a", true)
	if p == nil {
		// Refused before anything was copied: charged its size, it is
		// past the cap.
		if !strings.Contains(strings.Join(h.logs, "\n"), "more than the store's cap") {
			t.Fatalf("not published, for another reason:\n%s", strings.Join(h.logs, "\n"))
		}
		return
	}
	// p.Entry.Size is the version's allocated bytes (DiskUsage), taken
	// before Evict trims it: the copy wrote every hole.
	if p.Entry.Size > h.m.MaxSize {
		t.Errorf("a ~0-allocated sparse file was published as %s on disk, past MaxSize %s (then evicted: the disk was written first)", HumanSize(p.Entry.Size), HumanSize(h.m.MaxSize))
	}
}

// An overlay upper file's extended attributes do not survive mergeLayers's
// rename into the version: a later launch's overlay lower is the version
// itself, and a published copy holds plain files.
func TestPublishedFilesCarryNoXattrs(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	k := key("xa")
	seedTool(t, h, k)
	req, g := h.launch(t, "a", k)
	release, err := LockShared(g.Lock)
	if err != nil {
		t.Fatal(err)
	}
	f := upperOf(t, req, g, ".cache/tool/obj")
	write(t, f, big)
	if err := unix.Setxattr(f, "user.note", []byte(r2tok), 0); err != nil {
		t.Skipf("no user xattrs here: %v", err)
	}
	EndLaunch(g.Lock)
	release()
	p := h.finish(t, "a", true)
	if p == nil {
		t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
	}
	buf := make([]byte, 256)
	if n, err := unix.Getxattr(filepath.Join(p.Entry.Dir, "data", ".cache", "tool", "obj"), "user.note", buf); err == nil && strings.Contains(string(buf[:n]), r2tok) {
		t.Errorf("a run's xattr was published")
	}
}

// A copy restore conductor stopped in the middle of leaves a staging
// directory (a whole cache copy) and what it moved aside: the next restore
// into that home removes them. A staging directory in use (recent) stays.
func TestRestoreSweepsLeftovers(t *testing.T) {
	h := newHarness(t, false)
	k := key("sweep")
	seedTool(t, h, k)
	home := filepath.Join(h.homes, "kept")
	parent := filepath.Join(home, ".cache")
	write(t, filepath.Join(parent, restoreStagePrefix+"old", "blob"), big)
	write(t, filepath.Join(parent, restoreStagePrefix+"new", "blob"), "x")
	write(t, filepath.Join(parent, "tool"+movedAsideInfix+"abc"), "x")
	write(t, filepath.Join(parent, "other"+movedAsideInfix+"abc"), "x")
	old := time.Now().Add(-2 * staleStage)
	if err := os.Chtimes(filepath.Join(parent, restoreStagePrefix+"old"), old, old); err != nil {
		t.Fatal(err)
	}
	req := Request{Workspace: t.TempDir(), Home: home, Run: "b", Dispatch: "b", Key: k}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	g := h.restore(t, req)
	if len(g.Mounts) != 0 {
		t.Skip("restored by overlay")
	}
	for name, want := range map[string]bool{restoreStagePrefix + "old": false, restoreStagePrefix + "new": true,
		"tool" + movedAsideInfix + "abc": false, "other" + movedAsideInfix + "abc": true} {
		if _, err := os.Lstat(filepath.Join(parent, name)); (err == nil) != want {
			t.Errorf("%s: present %v, want %v", name, err == nil, want)
		}
	}
}

// The copy itself charges a sparse file its size, not its allocated
// blocks: past the budget it stops before writing the holes out.
func TestCopyFromHomeChargesSparseFilesTheirSize(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".cache", "tool", "small"), "x")
	f, err := os.Create(filepath.Join(home, ".cache", "tool", "sparse.img"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(256 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	budget := int64(64 << 20)
	if err := copyFromHome(home, ".cache/tool", filepath.Join(t.TempDir(), "dst"), &budget); err != errCopyCap {
		t.Fatalf("copy of a 256MiB sparse file under a 64MiB budget: %v, want errCopyCap", err)
	}
	if n := copyUsage(filepath.Join(home, ".cache", "tool")); n < 256<<20 {
		t.Fatalf("copyUsage %d, want at least the sparse file's size", n)
	}
}
