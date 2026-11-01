package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const memoryPackManifest = `
pack:
  name: acme-training
  version: 0.1.0
  requires:
    conductor: ">=0.1"
memory:
  entries: memory
`

const memoryEntryFile = `---
id: m0001
scope: standard:acme/api
created: "2026-09-01T10:00:00Z"
---

Prefer table-driven tests.
`

// writeMemoryPack writes a pack with one shipped memory entry.
func writeMemoryPack(t *testing.T, dir, rel, manifest string) {
	t.Helper()
	writePackSource(t, dir, rel, manifest)
	md := filepath.Join(dir, rel, "memory")
	if err := os.MkdirAll(md, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(md, "m0001.md"), []byte(memoryEntryFile), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMemConsumer(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPackShipsReadOnlyMemoryEntries(t *testing.T) {
	dir := t.TempDir()
	writeMemoryPack(t, dir, "src/training", memoryPackManifest)
	path := writeMemConsumer(t, dir, `
connectors: { box: { use: command } }
memory: { dir: `+filepath.Join(dir, "own-memory")+` }
packs:
  training:
    use: ./src/training
`)
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("a pack shipping memory entries must load: %v", err)
	}
	layers := cfg.PackMemoryLayers()
	if len(layers) != 1 {
		t.Fatalf("layers = %+v", layers)
	}
	l := layers[0]
	if l.Instance != "training" || l.Pack != "acme-training" || !filepath.IsAbs(l.Dir) {
		t.Fatalf("layer = %+v", l)
	}
	if _, err := os.Stat(filepath.Join(l.Dir, "m0001.md")); err != nil {
		t.Fatalf("the layer must point at the vendored entries: %v", err)
	}
	// The vendored copy, not the source: the source can move or change
	// without the daemon reading it.
	if strings.HasPrefix(l.Dir, filepath.Join(dir, "src")) {
		t.Fatalf("layer reads the pack SOURCE, not the vendored copy: %s", l.Dir)
	}
}

// A pack's memory: block may carry entries, never a backend.
func TestPackMemoryBackendStaysBindOnly(t *testing.T) {
	for _, tc := range []struct {
		name, block, want string
	}{
		{"dir backend", "memory: { dir: /var/lib/x }", "memory.dir"},
		{"store backend", "memory: { store: shared }", "memory.store"},
		{"type", "memory: { type: memory }", "memory.type"},
		{"entries plus a backend", "memory: { entries: memory, store: shared }", "memory.store"},
		{"a scalar", "memory: shared", "[memory]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMemoryPack(t, dir, "src/p", "pack: { name: p, version: 0.1.0, requires: { conductor: '>=0.1' } }\n"+tc.block+"\n")
			path := writeMemConsumer(t, dir, "packs:\n  p:\n    use: ./src/p\n")
			_, err := resolveAndLoad(t, path)
			if err == nil {
				t.Fatal("a memory backend in a pack must be rejected")
			}
			if !strings.Contains(err.Error(), "bind-only") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rejection should name %s as bind-only, got: %v", tc.want, err)
			}
		})
	}
}

func TestPackMemoryEntriesMustStayInsideThePack(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("---\nid: s\n---\n\nnot the pack's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, entries, want string
		setup               func(t *testing.T, packDir string)
	}{
		{"climbs out", "../../outside", "climbs out", nil},
		{"absolute", outside, "absolute", nil},
		{"home", "~/memory", "absolute", nil},
		{"missing", "nope", "nope", nil},
		{"a file", "conductor-pack.yaml", "not a directory", nil},
		{"empty", "''", "entries", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMemoryPack(t, dir, "src/p", "pack: { name: p, version: 0.1.0, requires: { conductor: '>=0.1' } }\nmemory: { entries: "+tc.entries+" }\n")
			path := writeMemConsumer(t, dir, "packs:\n  p:\n    use: ./src/p\n")
			_, err := resolveAndLoad(t, path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
}

// A symlink inside the pack that points out is refused at instantiate: the
// vendor copy drops symlinks, so this guards the resolver itself.
func TestResolvePackMemoryDirRefusesASymlinkOut(t *testing.T) {
	pack := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(pack, "memory")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := resolvePackMemoryDir(pack, "memory"); err == nil || !strings.Contains(err.Error(), "outside the pack") {
		t.Fatalf("a symlink out of the pack must be refused, got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pack, "real", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolvePackMemoryDir(pack, "real/deep")
	if err != nil || !strings.HasSuffix(got, filepath.Join("real", "deep")) {
		t.Fatalf("a nested dir inside the pack resolves: %q %v", got, err)
	}
}

// A pack fetched over git — the transport a private repo uses — ships its
// memory the same way: the entries come along in the vendored copy.
func TestGitFetchedPackShipsMemory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	writeMemoryPack(t, repo, "training", memoryPackManifest)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "training pack")

	dir := t.TempDir()
	path := writeMemConsumer(t, dir, `
connectors: { box: { use: command } }
memory: { dir: `+filepath.Join(dir, "own")+` }
packs:
  training:
    source: git::file://`+repo+`//training
`)
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("git-sourced memory pack: %v", err)
	}
	layers := cfg.PackMemoryLayers()
	if len(layers) != 1 {
		t.Fatalf("layers = %+v", layers)
	}
	if b, err := os.ReadFile(filepath.Join(layers[0].Dir, "m0001.md")); err != nil || !strings.Contains(string(b), "table-driven") {
		t.Fatalf("vendored entry: %v %q", err, b)
	}
}

func TestLintPackDirChecksMemoryEntries(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "p", memoryPackManifest) // no memory/ dir
	problems, err := LintPackDir(filepath.Join(dir, "p"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(problems, "\n"), "memory.entries") {
		t.Fatalf("lint should report the missing entries dir, got %v", problems)
	}
	writeMemoryPack(t, dir, "q", memoryPackManifest)
	problems, err = LintPackDir(filepath.Join(dir, "q"))
	if err != nil || len(problems) != 0 {
		t.Fatalf("a well-formed memory pack lints clean, got %v %v", problems, err)
	}
}
