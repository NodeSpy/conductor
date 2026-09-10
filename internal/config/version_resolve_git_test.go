package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackVersionConstraintResolvesTag proves end-to-end that a `version:`
// constraint drives which git tag a pack resolves to: a monorepo carrying
// component-prefixed tags (review-kit/v1.0.0, v1.1.0, v2.0.0) resolved with
// "~> 1.0" must pin v1.1.0's commit — the highest 1.x, never 2.0.0.
func TestPackVersionConstraintResolvesTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	writePackSource(t, repo, "review-kit", reviewKitManifest)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "review-kit v1.0.0")
	git("tag", "review-kit/v1.0.0")
	git("commit", "--allow-empty", "-m", "review-kit v1.1.0")
	git("tag", "review-kit/v1.1.0")
	wantSha := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "review-kit v2.0.0")
	git("tag", "review-kit/v2.0.0")

	dir := t.TempDir()
	body := `
connectors:
  gh: { use: github }
vaults:
  house: { type: file, dir: /tmp/pc-pack-vault }
steps:
  my-opus: { type: agent, name: my-opus, skill: { verbs: [github.submit_review] } }
packs:
  review:
    source: git::file://` + repo + `//review-kit
    version: "~> 1.0"
    connectors: { github: gh }
    stores: { cache: redis1 }
    secrets: { api_token: house/foocorp }
    steps: { reviewer: my-opus }
    triggers:
      on_review_request: { enabled: true, repos: [acme/app] }
stores:
  redis1: { type: boltdb, path: /tmp/pc-vc.db }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := ResolvePacks(path)
	if err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	if len(lock.Packs) != 1 {
		t.Fatalf("expected 1 lock entry, got %d", len(lock.Packs))
	}
	if got := lock.Packs[0].Resolved; got != wantSha {
		t.Fatalf("~> 1.0 resolved to %q, want v1.1.0's sha %q (must not pick v2.0.0)", got, wantSha)
	}
}
