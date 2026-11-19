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

// Credentials that live INSIDE a directory a tool also fills with bulk
// cache are never published with it, nor shown to a run restoring from the
// key: the protected list names the known ones, and a tree holding a git
// repository is refused whole.
func TestCredentialsInsideALearnedPathAreNotPublished(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		t.Run(fmt.Sprint("overlay=", overlay), func(t *testing.T) {
			h := newHarness(t, overlay)
			trusted, pr := Key{Template: "{{.repo}}", Value: "o/r"}, Key{Template: "pr-{{.repo}}-{{.pr}}", Value: "pr-o/r-9"}
			req, g := h.launch(t, "main", trusted)
			runWrites(t, req, g, map[string]string{
				".cache/huggingface/hub/model.bin": big,
				".cache/huggingface/token":         "hf_SECRET",
				".pulumi/plugins/p/bin":            big,
				".pulumi/credentials.json":         `{"accessTokens":{"https://api.pulumi.com":"pul-SECRET"}}`,
				".android/sdk/x":                   big,
				".android/adbkey":                  "-----BEGIN PRIVATE KEY-----",
				"src/dep/.git/config":              "[remote \"origin\"]\n\turl = https://x-access-token:ghs_SECRET@github.com/o/dep\n",
				"src/dep/blob":                     big,
			})
			p := h.finish(t, "main", true)
			if p == nil {
				t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
			}
			// A PR's fixer that restores from the trusted key.
			req2, g2 := h.launch(t, "pr", pr, trusted)
			var leaked []string
			for _, f := range []string{".cache/huggingface/token", ".pulumi/credentials.json", ".android/adbkey", "src/dep/.git/config"} {
				if b, err := os.ReadFile(lowerFile(req2, g2, f)); err == nil && (strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "PRIVATE")) {
					leaked = append(leaked, "~/"+f)
				}
			}
			if len(leaked) > 0 {
				t.Errorf("published %v and shown them to a PR run restoring from the trusted key (paths %v)", leaked, p.Entry.Paths)
			}
		})
	}
}

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

// A file a run filled that holds a credential value conductor gave the run
// is left out of the publish (logged by path, never by value); the rest of
// the path is published.
func TestFileHoldingAGivenCredentialIsLeftOut(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		t.Run(fmt.Sprint("overlay=", overlay), func(t *testing.T) {
			h := newHarness(t, overlay)
			const tok = "ghs_GIVENTOKEN1234567890abcdef"
			k := key("v")
			req, g := h.launch(t, "a", k)
			h.m.AddSecrets(g.Lock, []string{tok, "short"})
			// The value across the scan's chunk boundary is found too.
			edge := strings.Repeat("z", (1<<20)-5) + tok
			runWrites(t, req, g, map[string]string{
				".cache/tool/blob":         big,
				".cache/tool/log.txt":      "remote: https://x:" + tok + "@example.com\n",
				".cache/tool/edge.bin":     edge,
				".cache/tool/innocent.txt": "short short short",
			})
			p := h.finish(t, "a", true)
			if p == nil {
				t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
			}
			data := filepath.Join(p.Entry.Dir, "data", ".cache", "tool")
			for _, f := range []string{"log.txt", "edge.bin"} {
				if _, err := os.Lstat(filepath.Join(data, f)); err == nil {
					t.Errorf("%s holds a given credential and was published", f)
				}
			}
			for _, f := range []string{"blob", "innocent.txt"} {
				if _, err := os.Lstat(filepath.Join(data, f)); err != nil {
					t.Errorf("%s was left out: %v", f, err)
				}
			}
			logs := strings.Join(h.logs, "\n")
			if strings.Contains(logs, tok) {
				t.Errorf("a log line holds the credential:\n%s", logs)
			}
			if !strings.Contains(logs, "~/.cache/tool/log.txt holds a credential") {
				t.Errorf("the left-out file is not logged:\n%s", logs)
			}
		})
	}
}

// A launch that was given credentials whose values this conductor no
// longer holds (it restarted) is not published: its files cannot be
// checked.
func TestUnknownCredentialsAfterRestartRefusePublish(t *testing.T) {
	h := newHarness(t, true)
	k := key("restart")
	req, g := h.launch(t, "a", k)
	h.m.AddSecrets(g.Lock, []string{"a-credential-value"})
	runWrites(t, req, g, map[string]string{".cache/tool/blob": big})
	h.m.secrets = nil // a new conductor process: the ledger mark survives
	if p := h.finish(t, "a", true); p != nil {
		t.Fatalf("published %v with its credentials unknown", p.Entry.Paths)
	}
}

// A file named like a credential is left out only when its content looks
// like one: package trees keep their token.go, token.rs and cacert.pem.
func TestCredentialNamedFilesAreJudgedByContent(t *testing.T) {
	h := newHarness(t, true)
	k := key("names")
	req, g := h.launch(t, "a", k)
	runWrites(t, req, g, map[string]string{
		".cache/mod/blob":                   big,
		".cache/mod/x/oauth2@v1/token.go":   "package oauth2\n\ntype Token struct {\n\tAccessToken string `json:\"access_token\"`\n}\n",
		".cache/mod/syn/src/token.rs":       "pub struct Token;\n",
		".cache/mod/certifi/cacert.pem":     "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		".cache/mod/tokenizer.json":         `{"version":"1.0","added_tokens":[]}`,
		".cache/mod/keys/id_ed25519":        "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Bl\n-----END OPENSSH PRIVATE KEY-----\n",
		".cache/mod/keys/id_ed25519.pub":    "ssh-ed25519 AAAAC3Nza user@host\n",
		".cache/mod/state/credentials.json": `{"profiles":{"default":{"api_key":"abc"}}}`,
		".cache/mod/state/token":            "hf_abcdefghijklmnopqrstuvwxyz\n",
		".cache/mod/state/auth_token":       "0123456789abcdef0123456789abcdef\n",
		".cache/mod/state/server.key":       "-----BEGIN RSA PRIVATE KEY-----\n",
		".cache/mod/state/ghtoken-notes.md": "see ghp_abcdefghijklmnopqrstuvwxyz0123\n",
	})
	p := h.finish(t, "a", true)
	if p == nil {
		t.Fatalf("not published\n%s", strings.Join(h.logs, "\n"))
	}
	data := filepath.Join(p.Entry.Dir, "data", ".cache", "mod")
	for f, want := range map[string]bool{
		"x/oauth2@v1/token.go": true, "syn/src/token.rs": true, "certifi/cacert.pem": true, "tokenizer.json": true,
		"keys/id_ed25519.pub": true,
		"keys/id_ed25519":     false, "state/credentials.json": false, "state/token": false,
		"state/auth_token": false, "state/server.key": false, "state/ghtoken-notes.md": false,
	} {
		_, err := os.Lstat(filepath.Join(data, f))
		if got := err == nil; got != want {
			t.Errorf("%s published=%v, want %v", f, got, want)
		}
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
