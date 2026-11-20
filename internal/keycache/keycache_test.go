//go:build linux

package keycache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---- keys --------------------------------------------------------------

// A key's identity is its namespace, its template and its value: a
// template that renders an attacker's text can never equal, or publish
// over, another template's key — whatever the text.
func TestKeyIdentityIsNamespaced(t *testing.T) {
	shared := Key{Template: "{{.repo}}", Value: "acme/web"}
	pr := Key{Template: "{{.head_ref}}", Value: "acme/web"} // a branch named after the shared key
	pack := Key{Namespace: "autopilot", Template: "{{.repo}}", Value: "acme/web"}
	if shared.ID() == pr.ID() || shared.ID() == pack.ID() || pr.ID() == pack.ID() {
		t.Fatal("keys of different templates or namespaces share an id")
	}
	if shared.ID() != (Key{Template: "{{.repo}}", Value: "acme/web"}).ID() {
		t.Fatal("one key, two ids")
	}
	// The separator cannot be smuggled through the value or the template.
	a := Key{Template: "a", Value: "b\x00c"}
	b := Key{Template: "a\x00b", Value: "c"}
	if a.ID() == b.ID() {
		t.Fatal("ids collide across fields")
	}
	for _, v := range []string{"acme/web", "pr-acme/web#12", "v1.2+meta=x", "a:b@c"} {
		if err := CheckValue(v); err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
	}
	for _, v := range []string{"", "-x", "/abs", "a/../b", "a//b", "a/", "has space", "q\"uote", "new\nline", "glob*", "tab\t", "back\\slash", strings.Repeat("a", 201), "ünïcode", "../escape"} {
		if CheckValue(v) == nil {
			t.Errorf("%q accepted", v)
		}
	}
	if (Key{Template: "", Value: "x"}).Valid() == nil || (Key{Namespace: "a/b", Template: "t", Value: "x"}).Valid() == nil {
		t.Fatal("a key with no template or a bad namespace is valid")
	}
}

// The store never names a directory from a rendered value.
func TestStoreLayoutUsesIDsOnly(t *testing.T) {
	h := newHarness(t, true)
	k := Key{Template: "t", Value: "a/b/c"}
	if _, err := h.m.Store.Publish(k, []string{".cache/x"}, func(d string) error {
		write(t, filepath.Join(d, ".cache", "x", "f"), "x")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(h.m.Store.keysRoot())
	if len(ents) != 1 || ents[0].Name() != k.ID() {
		t.Fatalf("store keys: %v", ents)
	}
}

// ---- learning ----------------------------------------------------------

// The granularity rule: a candidate is a directory directly inside the home
// or an XDG base directory, never the home or a base directory whole; one
// holding a protected path is split into its children; protected paths and
// symlinks are never candidates.
func TestCandidatesGranularityAndExclusions(t *testing.T) {
	home := t.TempDir()
	for _, f := range []string{
		"go/pkg/mod/dep@v1/a.go", ".npm/_cacache/x", ".cache/yarn/v6/p", ".cache/go-build/ab/c",
		".local/share/pnpm/store/x", ".local/bin/tool", ".config/tool/conf", "Library/Caches/Yarn/x",
		"Library/Keychains/login.keychain", "Library/Preferences/p.plist",
		".cargo/registry/index/x", ".cargo/git/db/x", ".cargo/credentials.toml",
		".ssh/id_ed25519", ".gnupg/k", ".aws/credentials", ".config/gh/hosts.yml", ".claude/x", ".conductor/git-common/x",
		".local/share/fish/fish_history", "work/.git/HEAD",
	} {
		write(t, filepath.Join(home, f), "x")
	}
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "big"), big)
	if err := os.Symlink(elsewhere, filepath.Join(home, ".cache", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "linkedtop")); err != nil {
		t.Fatal(err)
	}
	got := candidates(home, protectedList([]string{".claude"}))
	want := []string{
		".cache/go-build", ".cache/yarn", ".cargo/git", ".cargo/registry", ".config/tool", ".local/bin",
		".local/share/pnpm", ".npm", "Library/Caches/Yarn", "go", "work",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates\n got %v\nwant %v", got, want)
	}
}

// A directory becomes one of a key's paths once MinRuns successful runs of
// the key each wrote MinSize there. Credentials and agent state never do,
// however much a run fills them; neither does what stays under the size.
func TestLearnThresholdsAndExclusions(t *testing.T) {
	h := newHarness(t, true)
	h.m.MinRuns = 2
	k := key("thresholds")
	files := map[string]string{
		".cache/fake/blob":         big,
		".cache/small/f":           "tiny",
		".ssh/id_ed25519":          big,
		".claude/projects/x":       big,
		".cargo/credentials.toml":  big,
		".cargo/registry/index/ab": big,
		".aws/credentials":         big,
		".config/gh/hosts.yml":     big,
		".local/share/fish/hist":   big,
		".conductor/git-common/x":  big,
	}
	req, g := h.launch(t, "d1", k)
	runWrites(t, req, g, files)
	if p := h.finish(t, "d1", true); p != nil {
		t.Fatalf("published after one run of two: %+v", p)
	}
	if l := h.m.Store.Learned(k.TemplateID()); len(l) != 0 {
		t.Fatalf("learned after one run: %v", l)
	}
	req, g = h.launch(t, "d2", k)
	runWrites(t, req, g, files)
	p := h.finish(t, "d2", true)
	if p == nil {
		t.Fatalf("nothing published after two runs\n%s", strings.Join(h.logs, "\n"))
	}
	want := []string{".cache/fake", ".cargo/registry"}
	if !slices.Equal(p.Entry.Paths, want) || !slices.Equal(h.m.Store.Learned(k.TemplateID()), want) {
		t.Fatalf("paths %v learned %v, want %v", p.Entry.Paths, h.m.Store.Learned(k.TemplateID()), want)
	}
	var found []string
	_ = filepath.Walk(filepath.Join(p.Entry.Dir, "data"), func(q string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			r, _ := filepath.Rel(filepath.Join(p.Entry.Dir, "data"), q)
			found = append(found, r)
		}
		return nil
	})
	if !slices.Equal(found, []string{".cache/fake/blob", ".cargo/registry/index/ab"}) {
		t.Fatalf("published files %v", found)
	}
}

// A learned path is never one a protected path meets, even when the
// learning state on disk says so.
func TestLearnedStateCannotNameAProtectedPath(t *testing.T) {
	h := newHarness(t, true)
	k := key("forged")
	if err := os.MkdirAll(h.m.Store.templateDir(k.TemplateID()), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, h.m.Store.learnFile(k.TemplateID()), `{"paths":[".ssh",".claude","..","/etc",".cache/ok"]}`)
	h.m.Store.Locked(func() {
		got := h.m.Store.learn(k.TemplateID(), nil, 1, 1, h.m.Protected)
		if !slices.Equal(got, []string{".cache/ok"}) {
			t.Fatalf("learned %v", got)
		}
	})
}

// ---- restore and publish -------------------------------------------------

// The end-to-end shape: a run fills a cache directory, its step succeeds
// and publishes; the next run of the key is shown what the first filled —
// by overlay or by copy.
func TestSecondRunRestoresWhatTheFirstFilled(t *testing.T) {
	for _, overlay := range []bool{true, false} {
		t.Run(fmt.Sprint("overlay=", overlay), func(t *testing.T) {
			h := newHarness(t, overlay)
			k := key("pr-1")
			req, g := h.launch(t, "d1", k)
			if len(g.Mounts) != 0 {
				t.Fatal("a miss mounted something")
			}
			runWrites(t, req, g, map[string]string{".cache/fake/pkg/a": big})
			if p := h.finish(t, "d1", true); p == nil {
				t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
			}
			req2, g2 := h.launch(t, "d2", k)
			if overlay && len(g2.Mounts) != 1 {
				t.Fatalf("mounts %+v", g2.Mounts)
			}
			if got := read(t, lowerFile(req2, g2, ".cache/fake/pkg/a")); got != big {
				t.Fatal("the second run does not see what the first filled")
			}
		})
	}
}

// A run publishes only to its own key. A restore_from key is read-only to
// it: the run is shown it, and what it writes over it goes to the run's
// own key, never back.
func TestPublishesOnlyToItsOwnKey(t *testing.T) {
	h := newHarness(t, true)
	shared, pr := key("shared"), key("pr-7")
	req, g := h.launch(t, "warm", shared)
	runWrites(t, req, g, map[string]string{".cache/fake/f": big, ".cache/fake/gone": big})
	if h.finish(t, "warm", true) == nil {
		t.Fatal("the shared key was not published")
	}
	before, _ := h.m.Store.Lookup(shared.ID())

	req, g = h.launch(t, "fix", pr, shared)
	if len(g.Mounts) != 1 || !strings.HasPrefix(g.Mounts[0].Lower, before.Dir) {
		t.Fatalf("not restored from the shared key: %+v", g.Mounts)
	}
	runWrites(t, req, g, map[string]string{".cache/fake/f": "changed by the PR's run", ".cache/fake/new": big})
	// The run deletes a file it was shown: an overlay whiteout.
	if err := setWhiteout(filepath.Join(g.Mounts[0].Upper, "gone")); err != nil {
		t.Skipf("no user xattrs here: %v", err)
	}
	p := h.finish(t, "fix", true)
	if p == nil || p.Key.ID() != pr.ID() {
		t.Fatalf("published %+v", p)
	}
	after, _ := h.m.Store.Lookup(shared.ID())
	if after.Version != before.Version || len(h.m.Store.versions(shared.ID())) != 1 {
		t.Fatal("the PR's run published to the shared key")
	}
	if read(t, filepath.Join(before.Dir, "data", ".cache", "fake", "f")) != big {
		t.Fatal("the PR's run changed the shared copy")
	}
	if _, err := os.Lstat(filepath.Join(before.Dir, "data", ".cache", "fake", "gone")); err != nil {
		t.Fatal("the PR's delete reached the shared copy")
	}
	data := filepath.Join(p.Entry.Dir, "data", ".cache", "fake")
	if read(t, filepath.Join(data, "f")) != "changed by the PR's run" || read(t, filepath.Join(data, "new")) != big {
		t.Fatal("the PR's key lacks its run's writes")
	}
	if _, err := os.Lstat(filepath.Join(data, "gone")); err == nil {
		t.Fatal("a deleted file came back")
	}
}

// A restore shows the key's newest copy; a newer one supersedes it, and a
// superseded copy goes as soon as no launch uses it.
func TestRestoreLatestAndSupersededGo(t *testing.T) {
	h := newHarness(t, true)
	k := key("latest")
	req, g := h.launch(t, "d1", k)
	runWrites(t, req, g, map[string]string{".cache/fake/v": "one" + big})
	v1 := h.finish(t, "d1", true)
	// A launch on v1, still running.
	reqL, gL := h.launch(t, "live", k)
	release, _ := LockShared(gL.Lock)
	req, g = h.launch(t, "d2", k)
	runWrites(t, req, g, map[string]string{".cache/fake/v": "two" + big})
	v2 := h.finish(t, "d2", true)
	if v1 == nil || v2 == nil || v2.Entry.Version <= v1.Entry.Version {
		t.Fatalf("versions %+v %+v", v1, v2)
	}
	if _, err := os.Lstat(v1.Entry.Dir); err != nil {
		t.Fatal("a superseded copy a live launch uses was removed")
	}
	if read(t, lowerFile(reqL, gL, ".cache/fake/v")) != "one"+big {
		t.Fatal("the live launch's copy changed")
	}
	_, g3 := h.launch(t, "d3", k)
	if !strings.HasPrefix(g3.Mounts[0].Lower, v2.Entry.Dir) {
		t.Fatalf("restored %s, not the newest", g3.Mounts[0].Lower)
	}
	release()
	h.m.Store.LaunchGrace = time.Nanosecond
	h.m.Evict()
	if _, err := os.Lstat(v1.Entry.Dir); err == nil {
		t.Fatal("a superseded copy outlived its last launch")
	}
}

// A failed run publishes nothing, and its layers go.
func TestFailedRunPublishesNothing(t *testing.T) {
	h := newHarness(t, true)
	k := key("fails")
	req, g := h.launch(t, "d1", k)
	runWrites(t, req, g, map[string]string{".cache/fake/half": big})
	if p := h.finish(t, "d1", false); p != nil {
		t.Fatalf("published %+v", p)
	}
	if len(h.m.Store.versions(k.ID())) != 0 || len(h.m.Store.Learned(k.TemplateID())) != 0 {
		t.Fatal("a failed run published or taught")
	}
	if _, err := os.Lstat(filepath.Dir(g.Lock)); err == nil {
		t.Fatal("a failed run's layers outlived its step")
	}
}

// A launch that never ran (no end mark) publishes nothing.
func TestALaunchThatNeverRanPublishesNothing(t *testing.T) {
	h := newHarness(t, true)
	req, _ := h.launch(t, "d1", key("never"))
	write(t, filepath.Join(req.Home, ".cache", "fake", "x"), big)
	if p := h.finish(t, "d1", true); p != nil {
		t.Fatalf("published %+v", p)
	}
}

// Two runs of one key publishing at once: the one that commits last is
// the newest, each copy is whole (never a mix of the two), and the store
// never holds a torn copy.
func TestConcurrentPublishLastWins(t *testing.T) {
	h := newHarness(t, true)
	k := key("racy")
	reqA, gA := h.launch(t, "A", k)
	reqB, gB := h.launch(t, "B", k)
	runWrites(t, reqA, gA, map[string]string{".cache/fake/who": "A" + big, ".cache/fake/a-only": big})
	runWrites(t, reqB, gB, map[string]string{".cache/fake/who": "B" + big, ".cache/fake/b-only": big})
	bDone := make(chan bool)
	var first sync.Mutex
	h.m.testHook = func(p string) {
		if p != "built" || !first.TryLock() {
			return
		}
		// A has built its copy; B builds and commits first.
		go func() {
			if pb, err := h.m.Finish(context.Background(), "B", true); err != nil || pb == nil {
				t.Errorf("B: %v %+v", err, pb)
			}
			close(bDone)
		}()
		<-bDone
	}
	pa, err := h.m.Finish(context.Background(), "A", true)
	if err != nil || pa == nil {
		t.Fatalf("A: %v %+v", err, pa)
	}
	newest, _ := h.m.Store.Lookup(k.ID())
	if newest.Version != pa.Entry.Version {
		t.Fatal("the run that committed last is not the newest")
	}
	data := filepath.Join(newest.Dir, "data", ".cache", "fake")
	if read(t, filepath.Join(data, "who")) != "A"+big {
		t.Fatal("torn copy")
	}
	if _, err := os.Lstat(filepath.Join(data, "b-only")); err == nil {
		t.Fatal("A's copy has B's file")
	}
	for _, e := range h.m.Store.versions(k.ID()) {
		if _, err := os.Lstat(filepath.Join(e.Dir, "meta.json")); err != nil {
			t.Fatalf("a version without its record: %s", e.Dir)
		}
	}
	if ents, _ := filepath.Glob(filepath.Join(h.m.Store.keyDir(k.ID()), ".tmp-*")); len(ents) != 0 {
		t.Fatalf("staging left: %v", ents)
	}
}

// Over the cap a copy is not published; beside the copies live launches
// use, a copy that does not fit the room left is not published either.
func TestOverTheCapIsNotPublished(t *testing.T) {
	h := newHarness(t, true)
	h.m.MaxSize = 8 << 10
	req, g := h.launch(t, "d1", key("huge"))
	runWrites(t, req, g, map[string]string{".cache/fake/a": big, ".cache/fake/b": big})
	if p := h.finish(t, "d1", true); p != nil {
		t.Fatalf("published over the cap: %+v", p)
	}
	if !slices.ContainsFunc(h.logs, func(s string) bool { return strings.Contains(s, "cap") }) {
		t.Fatalf("not logged: %v", h.logs)
	}

	h2 := newHarness(t, true)
	k := key("in-use")
	req, g = h2.launch(t, "d1", k)
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	if h2.finish(t, "d1", true) == nil {
		t.Fatal("not published")
	}
	_, gL := h2.launch(t, "live", k)
	release, _ := LockShared(gL.Lock)
	defer release()
	used, _ := h2.m.Store.Lookup(k.ID())
	h2.m.MaxSize = used.Size + 4<<10
	req, g = h2.launch(t, "d2", key("other"))
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	if p := h2.finish(t, "d2", true); p != nil {
		t.Fatalf("published past the room left: %+v", p)
	}
	if _, err := os.Lstat(used.Dir); err != nil {
		t.Fatal("the copy in use was evicted")
	}
}

// Finish waits (up to FinishWait) for a launch of the dispatch to stop,
// and publishes nothing from one still running.
func TestFinishWaitsForTheLaunch(t *testing.T) {
	h := newHarness(t, true)
	h.m.FinishWait = 300 * time.Millisecond
	req, g := h.launch(t, "d1", key("slow"))
	release, _ := LockShared(g.Lock)
	write(t, filepath.Join(req.Home, ".cache", "fake", "a"), big)
	if p := h.finish(t, "d1", true); p != nil {
		t.Fatal("published from a running launch")
	}
	if _, err := os.Lstat(filepath.Dir(g.Lock)); err != nil {
		t.Fatal("removed a running launch's layers")
	}
	h.m.FinishWait = 5 * time.Second
	go func() {
		time.Sleep(200 * time.Millisecond)
		EndLaunch(g.Lock)
		release()
	}()
	if p := h.finish(t, "d1", true); p == nil {
		t.Fatalf("not published once the launch stopped\n%s", strings.Join(h.logs, "\n"))
	}
}

// What a publish copies from a home is walked by descriptor: a symlink in
// it is copied as a link (never followed), a FIFO is skipped without
// blocking, and nothing in the store shares an inode with the home.
func TestPublishFromAHomeNeverFollowsOrLinks(t *testing.T) {
	h := newHarness(t, true)
	secret := t.TempDir()
	write(t, filepath.Join(secret, "key"), "SECRET"+big)
	req, g := h.launch(t, "d1", key("home"))
	cache := filepath.Join(req.Home, ".cache", "fake")
	write(t, filepath.Join(cache, "real"), big)
	if err := os.Symlink(secret, filepath.Join(cache, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(cache, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory linked in whole is never a candidate.
	if err := os.Symlink(secret, filepath.Join(req.Home, ".cache", "linked")); err != nil {
		t.Fatal(err)
	}
	runWrites(t, req, g, nil)
	done := make(chan *Published, 1)
	go func() { done <- h.finish(t, "d1", true) }()
	var p *Published
	select {
	case p = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("blocked on a FIFO in the home")
	}
	if p == nil || !slices.Equal(p.Entry.Paths, []string{".cache/fake"}) {
		t.Fatalf("published %+v", p)
	}
	data := filepath.Join(p.Entry.Dir, "data", ".cache", "fake")
	if l, err := os.Readlink(filepath.Join(data, "link")); err != nil || l != secret {
		t.Fatalf("the symlink: %q %v", l, err)
	}
	if _, err := os.Lstat(filepath.Join(data, "link", "key")); err == nil {
		fi, _ := os.Lstat(filepath.Join(data, "link"))
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("followed a symlink")
		}
	}
	if _, err := os.Lstat(filepath.Join(data, "fifo")); err == nil {
		t.Fatal("published a FIFO")
	}
	a, _ := os.Stat(filepath.Join(cache, "real"))
	b, _ := os.Stat(filepath.Join(data, "real"))
	if os.SameFile(a, b) {
		t.Fatal("the store shares an inode with the home")
	}
}

// A symlink on the way to a learned path refuses that path.
func TestCopyFromHomeRefusesASymlinkOnTheWay(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "fake", "f"), "x")
	if err := os.Symlink(elsewhere, filepath.Join(home, ".cache")); err != nil {
		t.Fatal(err)
	}
	budget := int64(1 << 30)
	if err := copyFromHome(home, ".cache/fake", filepath.Join(t.TempDir(), "out"), &budget); err == nil {
		t.Fatal("followed a symlink on the way")
	}
}

// ---- the ledger (the #266 properties) -----------------------------------

func published(t *testing.T, h *harness, k Key) Entry {
	t.Helper()
	req, g := h.launch(t, "seed-"+k.Value, k)
	runWrites(t, req, g, map[string]string{".cache/fake/f": big, ".cache/other/g": big})
	p := h.finish(t, "seed-"+k.Value, true)
	if p == nil {
		t.Fatalf("seed not published\n%s", strings.Join(h.logs, "\n"))
	}
	return p.Entry
}

// An overlay's upper and work directories reach mount(2) as paths, which
// follows a symlink: they are conductor's own, beside the store, never in
// the home or workspace. A restore writes nothing into the home.
func TestRestoreKeepsLayersOutOfTheHome(t *testing.T) {
	h := newHarness(t, true)
	k := key("layers")
	published(t, h, k)
	req, g := h.launch(t, "r1", k)
	if len(g.Mounts) != 2 {
		t.Fatalf("mounts %+v", g.Mounts)
	}
	for _, mt := range g.Mounts {
		for _, p := range []string{mt.Upper, mt.Work} {
			if within(p, req.Home) || within(p, req.Workspace) || !within(p, h.m.Store.Dir) {
				t.Errorf("layer %s is not conductor's alone", p)
			}
		}
	}
	if ents, _ := os.ReadDir(req.Home); len(ents) != 0 {
		t.Errorf("the restore wrote into the home: %v", ents)
	}
}

// Whether a copy is in use is conductor's own record, never a file an
// agent can write: a home's forged list does not pin it, and a FIFO in its
// place never blocks eviction.
func TestInUseNeverReadsAHome(t *testing.T) {
	h := newHarness(t, true)
	e := published(t, h, key("pinned"))
	for i, name := range []string{"forged", "fifo"} {
		d := filepath.Join(h.homes, name, ".conductor")
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			write(t, filepath.Join(d, "uses"), e.Dir+"\n")
		} else if err := syscall.Mkfifo(filepath.Join(d, "uses"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h.m.MaxSize = 1
	done := make(chan bool, 1)
	go func() { h.m.Evict(); done <- true }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("eviction blocked on a file in a home")
	}
	if left := h.m.Store.List(); len(left) != 0 {
		t.Fatalf("a home's forged list pinned %d cop(ies)", len(left))
	}
}

// A copy is in use for as long as the launch that overlays it runs, and
// no longer; an ended launch's layers go at the next eviction once they
// are past KeepEnded (or the store is over its cap).
func TestInUseFollowsTheLaunch(t *testing.T) {
	h := newHarness(t, true)
	k := key("follows")
	e := published(t, h, k)
	h.m.Store.LaunchGrace = time.Nanosecond
	_, g := h.launch(t, "kept", k)
	release, err := LockShared(g.Lock)
	if err != nil {
		t.Fatal(err)
	}
	h.m.MaxSize = 1
	h.m.Evict()
	if !h.m.InUse(e.Dir) || len(h.m.Store.List()) != 1 {
		t.Fatal("evicted a copy a running launch overlays")
	}
	EndLaunch(g.Lock)
	release()
	if h.m.InUse(e.Dir) {
		t.Fatal("a copy stays in use after its launch ended")
	}
	h.m.Evict()
	if left := h.m.Store.List(); len(left) != 0 {
		t.Fatalf("kept %d cop(ies) after the launch ended", len(left))
	}
	if _, err := os.Lstat(filepath.Dir(g.Lock)); err == nil {
		t.Error("an ended launch's layers outlived it over the cap")
	}
}

// An ended launch's layers wait for its step (KeepEnded) when the store
// has room, and go after.
func TestEndedLayersWaitForTheirStep(t *testing.T) {
	h := newHarness(t, true)
	h.m.Store.LaunchGrace = time.Nanosecond
	req, g := h.launch(t, "waits", key("waits"))
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	h.m.Evict()
	if _, err := os.Lstat(filepath.Dir(g.Lock)); err != nil {
		t.Fatal("an ended launch's layers went before its step ended")
	}
	h.m.Store.KeepEnded = time.Nanosecond
	h.m.Evict()
	if _, err := os.Lstat(filepath.Dir(g.Lock)); err == nil {
		t.Fatal("an ended launch's layers outlived KeepEnded")
	}
}

// Launches sharing a home each get their own layers and ledger entry.
func TestLaunchesSharingAHomeKeepTheirOwnLayers(t *testing.T) {
	h := newHarness(t, true)
	k := key("shared-home")
	published(t, h, k)
	h.m.Store.LaunchGrace = time.Nanosecond
	req, ga := h.launch(t, "A", k)
	relA, _ := LockShared(ga.Lock)
	defer relA()
	write(t, filepath.Join(ga.Mounts[0].Upper, "mine"), "A's")
	reqB := req
	reqB.Dispatch, reqB.Run = "B", "B"
	gb := h.restore(t, reqB)
	if ga.Lock == gb.Lock {
		t.Fatal("two launches share a ledger entry")
	}
	for i := range ga.Mounts {
		if ga.Mounts[i].Upper == gb.Mounts[i].Upper || ga.Mounts[i].Work == gb.Mounts[i].Work {
			t.Fatalf("two launches share a layer")
		}
	}
	if read(t, filepath.Join(ga.Mounts[0].Upper, "mine")) != "A's" {
		t.Fatal("B's restore reset A's upper layer")
	}
	// B's step ends; A's copy stays in use.
	if p := h.finish(t, "B", false); p != nil {
		t.Fatal("published")
	}
	h.m.MaxSize = 1
	h.m.Evict()
	for _, mt := range ga.Mounts {
		if _, err := os.Lstat(mt.Lower); err != nil {
			t.Fatalf("A's copy was evicted when B ended: %v", err)
		}
	}
	if read(t, filepath.Join(ga.Mounts[0].Upper, "mine")) != "A's" {
		t.Fatal("B's end removed A's layers")
	}
}

// A restore and an eviction never interleave.
func TestRestoreAndEvictAreOneCriticalSection(t *testing.T) {
	h := newHarness(t, true)
	k := key("race")
	published(t, h, k)
	req := Request{Workspace: t.TempDir(), Home: t.TempDir(), Run: "racer", Dispatch: "racer", Key: k}
	var g Grant
	restored := make(chan bool)
	h.m.testHook = func(point string) {
		if point != "evict-used" {
			return
		}
		h.m.testHook = nil
		go func() {
			var err error
			g, err = h.m.Restore(context.Background(), req)
			if err != nil {
				t.Error(err)
			}
			close(restored)
		}()
		select {
		case <-restored:
		case <-time.After(300 * time.Millisecond):
		}
	}
	h.m.MaxSize = 1
	h.m.Evict()
	<-restored
	for _, mt := range g.Mounts {
		if _, err := os.Lstat(mt.Lower); err != nil {
			t.Fatalf("granted a copy the eviction removed: %s", mt.Lower)
		}
	}
}

// A live launch's own writes never evict other copies.
func TestLaunchWritesDoNotEvictOtherCaches(t *testing.T) {
	h := newHarness(t, true)
	k := key("writes")
	e := published(t, h, k)
	_, g := h.launch(t, "writer", k)
	release, _ := LockShared(g.Lock)
	defer release()
	write(t, filepath.Join(g.Mounts[0].Upper, "big"), strings.Repeat("x", 2<<20))
	h.m.MaxSize = e.Size + 64<<10
	h.m.Evict()
	if n := len(h.m.Store.List()); n != 1 {
		t.Fatal("a launch's writes evicted a copy")
	}
}

// An ended launch's entry is swept holding its lock.
func TestSweepHoldsALaunchsLockThroughRemoval(t *testing.T) {
	h := newHarness(t, true)
	k := key("sweep")
	published(t, h, k)
	h.m.Store.LaunchGrace = time.Nanosecond
	_, g := h.launch(t, "late", k)
	got := make(chan error, 1)
	h.m.Store.testHook = func(p string) {
		if p != "sweep-locked" {
			return
		}
		go func() {
			release, err := LockShared(g.Lock)
			release()
			got <- err
		}()
		select {
		case err := <-got:
			t.Errorf("the launch took its lock while its layers were being swept (%v)", err)
			got <- err
		case <-time.After(300 * time.Millisecond):
		}
	}
	h.m.Evict()
	if err := <-got; err == nil {
		t.Fatal("the launch holds its lock over swept layers")
	}
}

// A copy restore copies without the store's lock, its version pinned.
func TestCopyRestoreOutsideTheStoreLock(t *testing.T) {
	h := newHarness(t, false)
	k := key("copy")
	published(t, h, k)
	var held, unpinned, copies int
	h.m.testHook = func(p string) {
		if p != "copy" {
			return
		}
		copies++
		release, ok := tryLock(filepath.Join(h.m.Store.Dir, storeLockName))
		release()
		if !ok {
			held++
		}
		h.m.mu.Lock()
		if len(h.m.pins) == 0 {
			unpinned++
		}
		h.m.mu.Unlock()
	}
	h.launch(t, "r", k)
	if copies == 0 || held != 0 || unpinned != 0 {
		t.Fatalf("copies %d held %d unpinned %d", copies, held, unpinned)
	}
	if len(h.m.pins) != 0 {
		t.Fatalf("pins left: %v", h.m.pins)
	}
}

// A restore never goes through a symlink in the home, and leaves the
// home's own content under a target alone (an overlay shadows it).
func TestRestoreRefusesSymlinkTargets(t *testing.T) {
	h := newHarness(t, true)
	k := key("symlinks")
	published(t, h, k)
	home := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".cache", "fake")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".cache", "other", "mine"), "the run's own")
	g := h.restore(t, Request{Workspace: t.TempDir(), Home: home, Run: "r", Dispatch: "r", Key: k})
	if len(g.Mounts) != 1 || g.Mounts[0].Target != filepath.Join(home, ".cache", "other") {
		t.Fatalf("mounts %+v", g.Mounts)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("wrote through the symlink: %v", ents)
	}
	if read(t, filepath.Join(home, ".cache", "other", "mine")) != "the run's own" {
		t.Fatal("the home's own content under a target changed")
	}
}

// A kept home: a file planted where a cache goes, or on the way, is moved
// aside, and the cache is still shown.
func TestAKeptHomeKeepsItsCaches(t *testing.T) {
	h := newHarness(t, true)
	k := key("kept")
	published(t, h, k)
	for name, plant := range map[string]string{"file-target": ".cache/fake", "file-on-the-way": ".cache"} {
		home := t.TempDir()
		write(t, filepath.Join(home, plant), "planted")
		g := h.restore(t, Request{Workspace: t.TempDir(), Home: home, Run: name, Dispatch: name, Key: k})
		if len(g.Mounts) == 0 {
			t.Fatalf("%s: a planted file turned the cache off\n%v", name, h.logs)
		}
		if fi, err := os.Lstat(filepath.Join(home, plant)); err != nil || !fi.IsDir() {
			t.Fatalf("%s: %v %v", name, fi, err)
		}
		if moved, _ := filepath.Glob(filepath.Join(home, plant+".conductor-moved-*")); len(moved) != 1 {
			t.Fatalf("%s: the planted file was not moved aside: %v", name, moved)
		}
	}
}

// By copy, a target with content is replaced by the cache, and a write to
// the copy never reaches the store. Over the copy cap, a miss.
func TestRestoreByCopy(t *testing.T) {
	h := newHarness(t, false)
	k := key("bycopy")
	e := published(t, h, k)
	home := t.TempDir()
	ro := filepath.Join(home, ".cache", "fake", "own")
	write(t, filepath.Join(ro, "x"), "its own")
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	g := h.restore(t, Request{Workspace: t.TempDir(), Home: home, Run: "c", Dispatch: "c", Key: k})
	if len(g.Mounts) != 0 {
		t.Fatal("overlay mounts without overlay")
	}
	p := filepath.Join(home, ".cache", "fake", "f")
	if read(t, p) != big {
		t.Fatal("not copied")
	}
	if _, err := os.Lstat(ro); err == nil {
		t.Fatal("the target's old content is still there")
	}
	write(t, p, "changed")
	if read(t, filepath.Join(e.Dir, "data", ".cache", "fake", "f")) != big {
		t.Fatal("a copy-restored write reached the store")
	}
	h.m.CopyMax = 1
	home2 := t.TempDir()
	h.restore(t, Request{Workspace: t.TempDir(), Home: home2, Run: "c2", Dispatch: "c2", Key: k})
	if _, err := os.Stat(filepath.Join(home2, ".cache", "fake", "f")); err == nil && !cloneSupported(t) {
		t.Fatal("copied past the cap")
	}
}

func cloneSupported(t *testing.T) bool {
	d := t.TempDir()
	write(t, filepath.Join(d, "a"), "x")
	ok, _ := cloneFile(filepath.Join(d, "a"), filepath.Join(d, "b"), 0o644)
	return ok || cloneDir(filepath.Join(d, "a"), filepath.Join(d, "c"))
}

func TestRemoveAllHandlesOverlayLeftovers(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "x", "work", "work", "incompat")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d, "x", "ro", "f"), "x")
	outside := filepath.Join(t.TempDir(), "o")
	write(t, outside, "x")
	_ = os.Chmod(outside, 0o400)
	if err := os.Symlink(outside, filepath.Join(d, "x", "ro", "link")); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(d, "x", "ro"), 0o500)
	_ = os.Chmod(filepath.Join(d, "x", "work", "work"), 0)
	if err := RemoveAll(filepath.Join(d, "x")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(outside); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("followed a symlink: %v %v", fi.Mode(), err)
	}
}

// What an older conductor's recipe caches left in the store is removed
// when the manager starts; this one's own entries are kept.
func TestStartRemovesTheRecipeCachesLayout(t *testing.T) {
	h := newHarness(t, true)
	e := published(t, h, key("kept-across"))
	for _, old := range []string{"acme~web/go/go-linux-abc/meta.json", ".warm/acme~web-x/tree/f", ".toobig/abc"} {
		write(t, filepath.Join(h.m.Store.Dir, old), "old")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := h.m.Start(ctx)
	cancel()
	<-done
	for _, old := range []string{"acme~web", ".warm", ".toobig"} {
		if _, err := os.Lstat(filepath.Join(h.m.Store.Dir, old)); err == nil {
			t.Errorf("%s left", old)
		}
	}
	if _, err := os.Lstat(e.Dir); err != nil {
		t.Fatal("removed this conductor's own copy")
	}
}

// Clearing a key removes its copies and what it learned, keeping a copy a
// running launch uses unless forced.
func TestClearKey(t *testing.T) {
	h := newHarness(t, true)
	k := key("clear")
	e := published(t, h, k)
	_, g := h.launch(t, "live", k)
	release, _ := LockShared(g.Lock)
	h.m.Store.Locked(func() {
		used := h.m.Store.Used()
		_, kept, err := h.m.Store.ClearKey(k.ID(), func(d string) bool { return used[d] }, false)
		if err != nil || !kept {
			t.Fatalf("kept %v err %v", kept, err)
		}
	})
	if _, err := os.Lstat(e.Dir); err != nil {
		t.Fatal("cleared a copy in use")
	}
	release()
	h.m.Store.Locked(func() {
		if _, kept, err := h.m.Store.ClearKey(k.ID(), func(string) bool { return false }, false); err != nil || kept {
			t.Fatalf("kept %v err %v", kept, err)
		}
	})
	if _, err := os.Lstat(h.m.Store.keyDir(k.ID())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the key's record is left")
	}
}

// A copy is on disk before it is named: the sync runs on the stage, before
// the commit.
func TestPublishSyncsBeforeCommit(t *testing.T) {
	h := newHarness(t, true)
	k := key("synced")
	var synced []string
	syncFS = func(d string) {
		synced = append(synced, d)
		if len(h.m.Store.versions(k.ID())) != 0 {
			t.Error("synced after the commit")
		}
		syncDir(d)
	}
	req, g := h.launch(t, "d1", k)
	runWrites(t, req, g, map[string]string{".cache/fake/a": big})
	if h.finish(t, "d1", true) == nil || len(synced) != 1 {
		t.Fatalf("published without a sync: %v", synced)
	}
}
