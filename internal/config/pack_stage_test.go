package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagedTinyPack is a minimal pack whose conductor floor and prompt the test
// varies between releases.
func stagedTinyPack(floor, prompt string) string {
	return `
pack:
  name: tiny
  version: "1.0.0"
  requires:
    conductor: "` + floor + `"
workflows:
  flow:
    steps:
      - id: say
        type: agent
        name: say
        workspace: local
        prompt: ` + prompt + `
`
}

// stageLeftovers lists the staging roots still under the config's .conductor/.
func stageLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, ".conductor", packStagePrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A staged resolution the config cannot load must leave the live vendor tree
// and lockfile exactly as they were (#194): the daemon keeps loading the last
// state that worked, and nothing is left behind once the stage is discarded.
// One that loads swaps in on Commit.
func TestPackStageTouchesNothingLiveUntilCommit(t *testing.T) {
	prev := runtimeVersion
	SetRuntimeVersion("v0.66.0")
	t.Cleanup(func() { SetRuntimeVersion(prev) })

	dir := t.TempDir()
	writePackSource(t, dir, "src/tiny", stagedTinyPack(">=0.1", "release one"))
	path := writeDoc(t, dir, `
connectors:
  hook: { use: webhook }
packs:
  tiny:
    source: ./src/tiny
`)
	if _, err := resolveAndLoad(t, path); err != nil {
		t.Fatalf("initial resolve: %v", err)
	}
	lockPath := filepath.Join(dir, LockfileName)
	vendored := filepath.Join(packVendorDir(dir), "tiny", PackManifestFile)
	liveLock, liveMan := readFile(t, lockPath), readFile(t, vendored)

	// A release this daemon cannot run.
	writePackSource(t, dir, "src/tiny", stagedTinyPack(">=99.0", "release two"))
	stage, err := StagePacks(path)
	if err != nil {
		t.Fatalf("StagePacks: %v", err)
	}
	if _, err := stage.Load(); err == nil || !strings.Contains(err.Error(), "requires conductor") {
		t.Fatalf("the staged release must not load, got %v", err)
	}
	if readFile(t, lockPath) != liveLock || readFile(t, vendored) != liveMan {
		t.Fatal("staging rewrote the live lockfile or vendor tree")
	}
	stage.Discard()
	if left := stageLeftovers(t, dir); len(left) != 0 {
		t.Fatalf("a discarded stage left %v behind", left)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("the live config must still load after a discarded stage: %v", err)
	}

	// A release that loads swaps in on Commit.
	writePackSource(t, dir, "src/tiny", stagedTinyPack(">=0.1", "release three"))
	stage, err = StagePacks(path)
	if err != nil {
		t.Fatalf("StagePacks: %v", err)
	}
	if _, err := stage.Load(); err != nil {
		t.Fatalf("staged load: %v", err)
	}
	if err := stage.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !strings.Contains(readFile(t, vendored), "release three") {
		t.Fatal("Commit did not swap the staged tree in")
	}
	if readFile(t, lockPath) == liveLock {
		t.Fatal("Commit did not write the staged lockfile")
	}
	if left := stageLeftovers(t, dir); len(left) != 0 {
		t.Fatalf("a committed stage left %v behind", left)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("the committed config must load: %v", err)
	}
}

// A config edit that adds a pack needs its packs resolved before it loads;
// StagePacksKeepingLocked resolves the new one and keeps every unchanged one
// at its locked revision, even when a newer release has appeared since.
func TestEditAddingAPackStagesItAndKeepsTheOthersLocked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repo := t.TempDir()
	writePackSource(t, repo, "tiny", stagedTinyPack(">=0.1", "release one"))
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "one")
	git("tag", "tiny/v1.0.0")
	locked := git("rev-parse", "HEAD")

	base := `
connectors:
  hook: { use: webhook }
packs:
  tiny:
    source: git::file://` + repo + `//tiny
    version: ">=1.0.0"
`
	path := writeDoc(t, dir, base)
	if _, err := resolveAndLoad(t, path); err != nil {
		t.Fatalf("initial resolve: %v", err)
	}
	if need, err := PacksNeedResolve(path); err != nil || need {
		t.Fatalf("a freshly resolved config needs resolving: %v %v", need, err)
	}

	// A newer release appears; the edit adds a second pack beside it.
	git("commit", "--allow-empty", "-m", "two")
	git("tag", "tiny/v1.1.0")
	writePackSource(t, dir, "src/other", strings.Replace(stagedTinyPack(">=0.1", "other"), "name: tiny", "name: other", 1))
	cand := filepath.Join(dir, "candidate.yaml")
	if err := os.WriteFile(cand, []byte(base+"  other:\n    source: ./src/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if need, err := PacksNeedResolve(cand); err != nil || !need {
		t.Fatalf("an added pack must need resolving: %v %v", need, err)
	}
	if _, err := Load(cand); err == nil {
		t.Fatal("the edit loaded before its pack was fetched")
	}
	st, err := StagePacksKeepingLocked(cand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err != nil {
		t.Fatalf("staged load: %v", err)
	}
	for _, e := range st.Lock.Packs {
		if e.Instance == "tiny" && e.Resolved != locked {
			t.Fatalf("tiny moved to %s; an unrelated edit must keep it at %s", e.Resolved, locked)
		}
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cand); err != nil {
		t.Fatalf("after commit the edit must load: %v", err)
	}
	if need, _ := PacksNeedResolve(cand); need {
		t.Fatal("still needs resolving after commit")
	}

	// Removing one is a change too; changing a version: is another.
	if need, _ := PacksNeedResolve(path); !need {
		t.Fatal("a removed pack must need resolving")
	}
	if err := os.WriteFile(cand, []byte(strings.Replace(base, `">=1.0.0"`, `"=1.1.0"`, 1)+"  other:\n    source: ./src/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if need, _ := PacksNeedResolve(cand); !need {
		t.Fatal("a changed version: must need resolving")
	}
}

// Removing the last pack commits an empty vendor tree and lockfile: the
// removed copy goes, and the config no longer needs resolving.
func TestEditRemovingTheLastPackDropsItsCopy(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/tiny", stagedTinyPack(">=0.1", "one"))
	path := writeDoc(t, dir, "connectors:\n  hook: { use: webhook }\npacks:\n  tiny:\n    source: ./src/tiny\n")
	if _, err := resolveAndLoad(t, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("connectors:\n  hook: { use: webhook }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if need, _ := PacksNeedResolve(path); !need {
		t.Fatal("a removed pack must need resolving")
	}
	st, err := StagePacksKeepingLocked(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(packVendorDir(dir), "tiny")); !os.IsNotExist(err) {
		t.Fatalf("the removed pack's copy is still vendored: %v", err)
	}
	if need, err := PacksNeedResolve(path); err != nil || need {
		t.Fatalf("still needs resolving: %v %v", need, err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}
