package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPackGitFetchEndToEnd proves the git transport end to end, hermetically:
// it builds a local git repo containing the pack in a subdir, then instantiates
// a config whose source is a `git::file://…//subdir` URL. This exercises the
// real `git clone` fetch path (not the local-copy path) and asserts the
// lockfile pins a concrete commit sha.
func TestPackGitFetchEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	// A pack living in review-kit/ inside the repo.
	writePackSource(t, repo, "review-kit", reviewKitManifest)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "add review-kit")

	dir := t.TempDir()
	body := `
connectors:
  gh: { use: github }
vaults:
  house: { type: file, dir: /tmp/pc-pack-vault }
agents:
  my-opus: { provider: claude, skill: { verbs: [github.submit_review] } }
packs:
  review:
    source: git::file://` + repo + `//review-kit
    connectors: { github: gh }
    stores: { cache: redis1 }
    secrets: { api_token: house/foocorp }
    agents: { reviewer: my-opus }
    triggers:
      on_review_request: { enabled: true, repos: [acme/app] }
stores:
  redis1: { type: boltdb, path: /tmp/pc-git.db }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, err := ResolvePacks(path)
	if err != nil {
		t.Fatalf("ResolvePacks (git): %v", err)
	}
	if len(lock.Packs) != 1 {
		t.Fatalf("expected 1 lock entry, got %d", len(lock.Packs))
	}
	if got := lock.Packs[0].Resolved; len(got) != 40 {
		t.Fatalf("git source should pin a 40-char commit sha, got %q", got)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load after git fetch: %v", err)
	}
	if _, ok := cfg.Workflows["review/review-flow"]; !ok {
		t.Fatalf("git-fetched pack did not instantiate; workflows=%v", workflowKeys(cfg))
	}
	tr := findTrigger(cfg, "review/on_review_request")
	if tr == nil || tr.Enabled == nil || !*tr.Enabled {
		t.Fatal("git-fetched trigger should be armed")
	}
}
