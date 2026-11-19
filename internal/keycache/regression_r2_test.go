//go:build linux || darwin

package keycache

// Regression tests from the second adversarial review of the step caches:
// each asserts a property a finding showed broken.

import (
	"encoding/base64"
	"fmt"
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

// F: the screen skips any file with nlink > 1 ("hard-linked from the
// restored version"), but an overlay upper keeps the agent's own hard
// links, and mergeLayers renames them into the version as they are.
func TestHardLinkedNewFileIsStillScreened(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	k := key("hl")
	seedTool(t, h, k)
	req, g := h.launch(t, "a", k)
	if len(g.Mounts) == 0 {
		t.Fatal("precondition: no overlay mount")
	}
	h.m.AddSecrets(g.Lock, []string{r2tok})
	release, err := LockShared(g.Lock)
	if err != nil {
		t.Fatal(err)
	}
	f := upperOf(t, req, g, ".cache/tool/run.log")
	write(t, f, "remote: https://x:"+r2tok+"@github.com/o/r\n")
	if err := os.Link(f, f+".1"); err != nil { // the agent: ln run.log run.log.1
		t.Fatal(err)
	}
	write(t, upperOf(t, req, g, ".cache/tool/more"), big)
	EndLaunch(g.Lock)
	release()
	p := h.finish(t, "a", true)
	if p == nil {
		t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
	}
	if b, err := os.ReadFile(filepath.Join(p.Entry.Dir, "data", ".cache", "tool", "run.log")); err == nil && strings.Contains(string(b), r2tok) {
		t.Errorf("a new file holding a given credential was published: the agent hard-linked it once, so the screen took it for the restored version's")
	}
}

// F: what the screen does not look at — a symlink's target, a file's
// name, and the value encoded the way tools store it (base64 of
// user:token, as in a Basic auth header or docker/npm auth fields).
func TestCredentialOutsideFileBytesIsLeftOut(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		t.Run(fmt.Sprint("overlay=", overlay), func(t *testing.T) {
			h := newHarness(t, overlay)
			k := key("enc")
			req, g := h.launch(t, "a", k)
			h.m.AddSecrets(g.Lock, []string{r2tok})
			release, err := LockShared(g.Lock)
			if err != nil {
				t.Fatal(err)
			}
			d := filepath.Join(req.Home, ".cache", "tool")
			write(t, filepath.Join(d, "blob"), big)
			write(t, filepath.Join(d, "auth.json"), `{"auth":"`+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+r2tok))+`"}`)
			write(t, filepath.Join(d, "by-name", r2tok), "x")
			if err := os.Symlink("/nonexistent/"+r2tok, filepath.Join(d, "link")); err != nil {
				t.Fatal(err)
			}
			EndLaunch(g.Lock)
			release()
			p := h.finish(t, "a", true)
			if p == nil {
				t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
			}
			data := filepath.Join(p.Entry.Dir, "data", ".cache", "tool")
			var leaked []string
			if b, err := os.ReadFile(filepath.Join(data, "auth.json")); err == nil && strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("x-access-token:"+r2tok))) {
				leaked = append(leaked, "base64 in auth.json")
			}
			if _, err := os.Lstat(filepath.Join(data, "by-name", r2tok)); err == nil {
				leaked = append(leaked, "a file name")
			}
			if l, err := os.Readlink(filepath.Join(data, "link")); err == nil && strings.Contains(l, r2tok) {
				leaked = append(leaked, "a symlink target")
			}
			if len(leaked) > 0 {
				t.Errorf("a given credential was published in: %v", leaked)
			}
		})
	}
}

// F: the restart refusal ("credentials unknown → not published") is
// bypassed by a resume after the restart: takeOver copies the mark,
// the resumed launch's AddSecrets makes the launch "known" again, and
// launch 1's upper (written with the pre-restart value) is published
// unscanned for it.
func TestResumeAfterRestartIsNotPublished(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	k := key("rs")
	seedTool(t, h, k)
	const t1, t2 = "ghs_BEFORE_RESTART_0123456789abc", "ghs_AFTER__RESTART_0123456789abc"
	req1, g1 := h.launch(t, "d", k)
	if len(g1.Mounts) == 0 {
		t.Fatal("precondition: no overlay mount")
	}
	h.m.AddSecrets(g1.Lock, []string{t1})
	runWrites(t, req1, g1, map[string]string{".cache/tool/run.log": "token " + t1 + "\n"})
	h.m.secrets, h.m.homeVals = nil, nil // conductor restarts: the values are gone
	g2 := h.restore(t, req1)             // the session resumes: same dispatch, home, key
	h.m.AddSecrets(g2.Lock, []string{t2})
	runWrites(t, req1, g2, map[string]string{".cache/tool/more": big})
	p := h.finish(t, "d", true)
	if p == nil {
		return // refused: the property holds
	}
	if b, err := os.ReadFile(filepath.Join(p.Entry.Dir, "data", ".cache", "tool", "run.log")); err == nil && strings.Contains(string(b), t1) {
		t.Errorf("published launch 1's file holding its (pre-restart, unknown) credential after a resume")
	}
}

// F (pre-existing, reached via the new scan): a sparse file is budgeted by
// its allocated blocks but copied with io.Copy, which writes every hole:
// the published version takes its full apparent size, past MaxSize.
func TestSparseFileIsChargedItsSize(t *testing.T) {
	h := newHarness(t, false)
	h.m.MaxSize = 64 << 20
	k := key("sparse")
	req, g := h.launch(t, "a", k)
	h.m.AddSecrets(g.Lock, []string{r2tok})
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

// F: an overlay upper file's extended attributes survive mergeLayers's
// rename into the version, and a later launch's overlay lower is the
// version itself: an xattr value is neither screened nor stripped.
func TestPublishedFilesCarryNoXattrs(t *testing.T) {
	needsMerge(t)
	h := newHarness(t, true)
	k := key("xa")
	seedTool(t, h, k)
	req, g := h.launch(t, "a", k)
	h.m.AddSecrets(g.Lock, []string{r2tok})
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
		t.Errorf("a given credential was published in an xattr")
	}
}

// F (low): the git-tree refusal matches only an entry named .git; a bare
// or mirror clone (repo.git/config, hooks/) holding a credential conductor
// did not give the run is published.
func TestBareCloneIsNotPublished(t *testing.T) {
	h := newHarness(t, false)
	k := key("bare")
	req, g := h.launch(t, "a", k)
	runWrites(t, req, g, map[string]string{
		".cache/tool/blob":                           big,
		".cache/tool/mirror.git/HEAD":                "ref: refs/heads/main\n",
		".cache/tool/mirror.git/config":              "[remote \"origin\"]\n\turl = https://u:glpat-NOTGIVENBYCONDUCTOR0123@gitlab.example/o/r\n\tmirror = true\n",
		".cache/tool/mirror.git/hooks/post-checkout": "#!/bin/sh\ncurl evil\n",
	})
	p := h.finish(t, "a", true)
	if p == nil {
		return
	}
	if _, err := os.Lstat(filepath.Join(p.Entry.Dir, "data", ".cache", "tool", "mirror.git", "config")); err == nil {
		t.Errorf("a bare clone's config (credential in its remote URL) and hooks were published; only a .git entry is refused")
	}
}

// A kept home an earlier conductor process gave a launch credentials in:
// those values are gone after a restart, so what the home holds cannot be
// checked for them — its publishes are refused, until the home is gone.
func TestKeptHomeAfterRestartIsNotPublished(t *testing.T) {
	h := newHarness(t, false)
	k := key("kept")
	req, g := h.launch(t, "a", k)
	h.m.AddSecrets(g.Lock, []string{r2tok})
	runWrites(t, req, g, map[string]string{".cache/tool/blob": big, ".cache/tool/left": "remote " + r2tok + "\n"})
	if h.finish(t, "a", true) == nil {
		t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
	}
	// conductor restarts: a new manager over the same store.
	old := h.m
	h.m = &Manager{Store: &Store{Dir: old.Store.Dir}, Protected: old.Protected, MaxSize: old.MaxSize, MinSize: old.MinSize,
		MinRuns: old.MinRuns, FinishWait: old.FinishWait, Overlay: old.Overlay, Logf: old.Logf}
	req2 := req
	req2.Dispatch, req2.Run = "b", "b"
	g2 := h.restore(t, req2)
	runWrites(t, req2, g2, map[string]string{".cache/tool/more": big})
	if p := h.finish(t, "b", true); p != nil {
		t.Fatalf("published from a home an earlier process gave credentials in: %+v", p.Entry)
	}
	if !strings.Contains(strings.Join(h.logs, "\n"), "an earlier conductor gave runs in this home credentials") {
		t.Fatalf("no reason logged:\n%s", strings.Join(h.logs, "\n"))
	}
	// The home removed: its mark goes, and a new home publishes.
	if err := RemoveAll(req.Home); err != nil {
		t.Fatal(err)
	}
	h.m.Evict()
	if ents, _ := os.ReadDir(filepath.Join(h.m.Store.Dir, homesDirName)); len(ents) != 0 {
		t.Fatalf("a gone home's mark is kept: %v", ents)
	}
	req3, g3 := h.launch(t, "c", k)
	runWrites(t, req3, g3, map[string]string{".cache/tool/blob": big})
	if h.finish(t, "c", true) == nil {
		t.Fatalf("a fresh home did not publish\n%s", strings.Join(h.logs, "\n"))
	}
}

// Go's module cache keeps bare clones (pkg/mod/cache/vcs): one with no
// credential and no live hook is published; one with a credential in its
// config, or a hook, is not.
func TestVCSCacheBareRepos(t *testing.T) {
	repo := func(cfg string, hook bool) map[string]string {
		vcs := ".cache/go/pkg/mod/cache/vcs/0123abcd/"
		f := map[string]string{
			".cache/go/pkg/mod/blob":      big,
			vcs + "HEAD":                  "ref: refs/heads/main\n",
			vcs + "config":                cfg,
			vcs + "objects/pack/x":        "pack",
			vcs + "hooks/pre-push.sample": "#!/bin/sh\n",
		}
		if hook {
			f[vcs+"hooks/post-checkout"] = "#!/bin/sh\ncurl evil\n"
		}
		return f
	}
	plain := "[core]\n\tbare = true\n[remote \"origin\"]\n\turl = https://github.com/o/r\n"
	for name, c := range map[string]struct {
		files map[string]string
		kept  bool
	}{
		"plain":       {repo(plain, false), true},
		"userinfo":    {repo("[remote \"origin\"]\n\turl = https://u:p@github.com/o/r\n", false), false},
		"extraheader": {repo(plain+"[http]\n\textraheader = AUTHORIZATION: basic eA==\n", false), false},
		"a live hook": {repo(plain, true), false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, false)
			req, g := h.launch(t, "a", key("vcs-"+strings.ReplaceAll(name, " ", "-")))
			runWrites(t, req, g, c.files)
			p := h.finish(t, "a", true)
			kept := p != nil
			if kept {
				_, err := os.Lstat(filepath.Join(p.Entry.Dir, "data", ".cache", "go", "pkg", "mod", "cache", "vcs", "0123abcd", "config"))
				kept = err == nil
			}
			if kept != c.kept {
				t.Fatalf("kept %v, want %v\n%s", kept, c.kept, strings.Join(h.logs, "\n"))
			}
		})
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

// Learning is kept per family: a key restoring from another learns with
// that one's family, and two families of one template (two repositories'
// shared keys, say) never count toward each other's paths.
func TestLearningIsPerFamily(t *testing.T) {
	h := newHarness(t, false)
	h.m.MinRuns = 2
	a := Key{Template: "{{.x}}", Value: "a"}
	a.Family = a.ID()
	b := Key{Template: "{{.x}}", Value: "b"}
	b.Family = b.ID()
	if a.TemplateID() == b.TemplateID() {
		t.Fatal("two families share learning")
	}
	pr := Key{Template: "pr-{{.x}}-{{.n}}", Value: "pr-a-1", Family: a.ID()}
	pr2 := Key{Template: "pr-{{.x}}-{{.n}}", Value: "pr-a-2", Family: a.ID()}
	if pr.TemplateID() != pr2.TemplateID() {
		t.Fatal("one family's per-run keys do not share learning")
	}
	// One run of a, one of b: neither family has two runs yet.
	for _, k := range []Key{a, b} {
		req, g := h.launch(t, k.Value, k)
		runWrites(t, req, g, map[string]string{".cache/tool/blob": big})
		if h.finish(t, k.Value, true) != nil {
			t.Fatalf("%s published after one run of its family", k.Value)
		}
	}
	req, g := h.launch(t, "a2", a)
	runWrites(t, req, g, map[string]string{".cache/tool/blob": big})
	if h.finish(t, "a2", true) == nil {
		t.Fatalf("a's second run did not publish\n%s", strings.Join(h.logs, "\n"))
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
