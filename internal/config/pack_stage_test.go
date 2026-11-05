package config

import (
	"os"
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
