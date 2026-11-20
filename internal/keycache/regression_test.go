package keycache

// Regression tests for the adversarial review of the step caches (#271):
// each asserts a property a finding showed broken.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A key with no published version and no launch for KeyIdle loses its
// directory: per-PR keys do not leave one directory per PR forever.
func TestIdleKeyDirectoriesAreReclaimed(t *testing.T) {
	h := newHarness(t, true)
	h.m.MaxSize = 1 // evict every version
	h.m.Store.KeyIdle = time.Nanosecond
	for i := 0; i < 40; i++ {
		d := fmt.Sprintf("d%d", i)
		req, g := h.launch(t, d, key(fmt.Sprintf("pr-%d", i)))
		runWrites(t, req, g, map[string]string{".cache/fake/a": "small"})
		h.finish(t, d, true)
	}
	h.m.Evict()
	ents, _ := os.ReadDir(h.m.Store.keysRoot())
	n := 0
	for _, e := range ents {
		if idRE.MatchString(e.Name()) {
			n++
		}
	}
	if len(h.m.Store.List()) == 0 && n > 0 {
		t.Errorf("no published version left, but %d key directories (key.json/learn.json) remain; nothing ever removes them", n)
	}
	// A key a launch still names keeps its directory, idle or not.
	k := key("live")
	h.launch(t, "live", k)
	h.m.Evict()
	if _, err := os.Lstat(h.m.Store.keyDir(k.ID())); err != nil {
		t.Errorf("a key a live launch names lost its directory: %v", err)
	}
}

// Learning is per template, not per rendered key. A key that is new for
// every PR (the pack's pr-{{.repo}}-{{.pr}}) starts from what the step's
// earlier PRs learned: with the default min_runs=2, a new PR's first
// successful fixer publishes, and its second gets a hit.
func TestPerRunKeyHitsOnItsSecondRun(t *testing.T) {
	h := newHarness(t, true)
	h.m.MinRuns = DefaultMinRuns
	run := func(k Key, d string, i int) bool {
		req, g := h.launch(t, d, k)
		runWrites(t, req, g, map[string]string{".cache/go-build/x": big, fmt.Sprintf(".cache/go-build/y%d", i): big})
		h.finish(t, d, true)
		return len(g.Mounts) > 0
	}
	// Two PRs, one fixer each: the template learns ~/.cache/go-build.
	run(key("pr-o/r-1"), "a", 0)
	run(key("pr-o/r-2"), "b", 1)
	k := key("pr-o/r-3")
	hits := []bool{run(k, "c0", 2), run(k, "c1", 3)}
	if hits[0] || !hits[1] {
		t.Errorf("a new PR's fixers' hits: %v, want [false true] (its first run publishes what the template learned)", hits)
	}
}

// Store size counts a file hard-linked into several versions once: a key
// built over its restore_from key's copy does not use the cap twice.
func TestStoreSizeCountsHardLinksOnce(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	base := key("base")
	req, g := h.launch(t, "a", base)
	runWrites(t, req, g, map[string]string{".cache/fake/blob": strings.Repeat("y", 1<<20)})
	h.finish(t, "a", true)
	one := h.m.Store.Size()
	for i := 0; i < 4; i++ {
		d := fmt.Sprintf("pr%d", i)
		req, g := h.launch(t, d, key(d), base)
		if len(g.Mounts) != 1 {
			t.Fatalf("mounts %+v", g.Mounts)
		}
		runWrites(t, req, g, map[string]string{".cache/fake/small": "s"})
		if h.finish(t, d, true) == nil {
			t.Fatal("not published")
		}
	}
	if got := h.m.Store.Size(); got > one+one/2 {
		t.Errorf("store size %s after 4 keys built over one %s copy: hard links counted per version", HumanSize(got), HumanSize(one))
	}
}

// A setuid/setgid bit in a run's upper layer never survives into the
// published copy (mergeLayers renames the file in, then masks it, as
// copyFromHome does).
func TestMergeDropsSetuid(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	k := key("s")
	req, g := h.launch(t, "a", k)
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	h.finish(t, "a", true)
	req, g = h.launch(t, "b", k)
	if len(g.Mounts) != 1 {
		t.Fatalf("mounts %+v", g.Mounts)
	}
	runWrites(t, req, g, map[string]string{".cache/fake/tool": big})
	if err := os.Chmod(filepath.Join(g.Mounts[0].Upper, "tool"), 0o4755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(g.Mounts[0].Upper, "tool"), os.ModeSetuid|0o755)
	p := h.finish(t, "b", true)
	if p == nil {
		t.Fatal("not published")
	}
	fi, err := os.Lstat(filepath.Join(p.Entry.Dir, "data", ".cache", "fake", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSetuid != 0 {
		t.Errorf("published copy keeps setuid: %v", fi.Mode())
	}
}

// Copy restore (macOS, or Linux without unprivileged overlay) into a home
// another live launch shares: a process in the home swapping a parent for
// a symlink while conductor restores never steers conductor's writes
// outside the home (copyIn works by descriptor from the home down).
func TestCopyRestoreRaceStaysInTheHome(t *testing.T) {
	h := newHarness(t, false)
	k := key("race")
	req, g := h.launch(t, "seed", k)
	runWrites(t, req, g, map[string]string{".cache/fake/payload": big})
	if h.finish(t, "seed", true) == nil {
		t.Fatal("not published")
	}
	home := filepath.Join(h.homes, "shared")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	done := make(chan struct{})
	go func() { // the other launch's agent, in the shared home
		defer close(done)
		c, real := filepath.Join(home, ".cache"), filepath.Join(home, ".cache-real")
		for !stop.Load() {
			if os.Rename(c, real) == nil {
				_ = os.Symlink(outside, c)
				time.Sleep(200 * time.Microsecond)
				_ = os.Remove(c)
				_ = os.Rename(real, c)
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	escaped := false
	for i := 0; time.Now().Before(deadline) && !escaped; i++ {
		_ = RemoveAll(filepath.Join(home, ".cache-real", "fake"))
		_ = RemoveAll(filepath.Join(home, ".cache", "fake"))
		_ = os.Mkdir(filepath.Join(outside, "fake"), 0o755) // os.Remove(target) must find something to remove
		r := Request{Workspace: t.TempDir(), Home: home, Run: "r", Dispatch: fmt.Sprintf("r%d", i), Key: k}
		_, _ = h.m.Restore(context.Background(), r)
		if _, err := os.Lstat(filepath.Join(outside, "fake", "payload")); err == nil {
			escaped = true
		}
	}
	stop.Store(true)
	<-done
	if escaped {
		t.Errorf("a copy restore wrote cache content outside the home (%s/fake/payload)", outside)
	}
}

// Two launches of one dispatch (a resume, an output_schema re-run) in one
// home: the second takes over the first's overlay layers, so it sees what
// the first wrote under an overlaid path, and Finish publishes both
// launches' writes.
func TestResumeKeepsFirstLaunchOverlayWrites(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	k := key("resume")
	req, g := h.launch(t, "seed", k)
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	h.finish(t, "seed", true)

	req1, g1 := h.launch(t, "d", k)
	runWrites(t, req1, g1, map[string]string{".cache/fake/built-by-launch-1": big})
	req2 := req1
	g2 := h.restore(t, req2) // the resume: same dispatch, same home
	seen := false
	if _, err := os.Lstat(filepath.Join(g2.Mounts[0].Lower, "built-by-launch-1")); err == nil {
		seen = true
	}
	if _, err := os.Lstat(filepath.Join(g2.Mounts[0].Upper, "built-by-launch-1")); err == nil {
		seen = true
	}
	runWrites(t, req2, g2, map[string]string{".cache/fake/built-by-launch-2": big})
	p := h.finish(t, "d", true)
	if p == nil {
		t.Fatal("not published")
	}
	_, err := os.Lstat(filepath.Join(p.Entry.Dir, "data", ".cache", "fake", "built-by-launch-1"))
	if !seen || err != nil {
		t.Errorf("launch 1's overlay writes: visible to the resume=%v, published=%v", seen, err == nil)
	}
}

// A copy restore in progress pins its version on disk: another process
// (`conductor sandbox caches clear`) sees it in use.
func TestCopyRestorePinsOnDisk(t *testing.T) {
	h := newHarness(t, false)
	k := key("pin")
	req, g := h.launch(t, "seed", k)
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	p := h.finish(t, "seed", true)
	if p == nil {
		t.Fatal("not published")
	}
	other := &Store{Dir: h.m.Store.Dir}
	seen := false
	h.m.testHook = func(point string) {
		if point == "copy" {
			seen = other.Used()[p.Entry.Dir]
		}
	}
	h.launch(t, "copy", k)
	if !seen {
		t.Error("a copy restore's version is not in use on disk while it copies")
	}
	if other.Used()[p.Entry.Dir] {
		t.Error("the version is still in use on disk after the copy")
	}
}

// needsMerge skips where an overlay launch's layers are not merged on
// publish (no overlay mounts but on Linux).
func needsMerge(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("overlay layers are merged on Linux only")
	}
}
