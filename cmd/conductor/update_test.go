package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// writeFakeConductor writes a shell script standing in for the downloaded
// release binary's `validate` subcommand, so preflightValidate can be tested
// without a real network fetch.
func writeFakeConductor(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "conductor")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPreflightValidatePassesThroughSuccess: a downloaded binary whose
// `validate` exits 0 against the daemon's config preflights clean.
func TestPreflightValidatePassesThroughSuccess(t *testing.T) {
	bin := writeFakeConductor(t, "exit 0")
	if err := preflightValidate(bin, "/any/config.yaml", true); err != nil {
		t.Fatalf("preflightValidate: %v", err)
	}
}

// TestPreflightValidateReportsFailureWithOutputTail: the self-update
// preflight (5a) — a release that cannot load this box's config must be
// refused, with enough of its own explanation to act on, not just "exit
// status 1".
func TestPreflightValidateReportsFailureWithOutputTail(t *testing.T) {
	bin := writeFakeConductor(t, `echo "config: the legacy integrations: block is no longer supported" >&2; exit 1`)
	err := preflightValidate(bin, "/any/config.yaml", true)
	if err == nil {
		t.Fatal("want an error when the downloaded binary's validate fails")
	}
	if !strings.Contains(err.Error(), "legacy integrations") {
		t.Fatalf("want the validate failure's own output in the error, got: %v", err)
	}
}

// TestPreflightValidateTimesOut: a hung validate (a stuck network check in a
// plugin-fetchability probe, say) must not hang the whole update.
func TestPreflightValidateTimesOut(t *testing.T) {
	old := selfUpdatePreflightTimeout
	selfUpdatePreflightTimeout = 150 * time.Millisecond
	t.Cleanup(func() { selfUpdatePreflightTimeout = old })

	// `exec` replaces the shell process in place (no forked grandchild) so
	// killing the single pid on timeout closes its stdout/stderr pipes
	// immediately — exactly how a real (single-process) downloaded binary
	// behaves. A plain "sleep 5" would fork sleep as a child of the shell:
	// killing only the shell leaves the orphaned sleep holding the output
	// pipe open, and CombinedOutput would hang for the full sleep regardless
	// of the timeout.
	bin := writeFakeConductor(t, "exec sleep 5")
	start := time.Now()
	err := preflightValidate(bin, "/any/config.yaml", true)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("preflightValidate took %s — selfUpdatePreflightTimeout did not bound it", elapsed)
	}
}

// TestSaveRollbackCopyWritesPrevBinary: 5b — a manual rollback must be
// possible with no re-fetch. saveRollbackCopy must leave the PREVIOUS
// binary's exact bytes at <exe>.prev.
func TestSaveRollbackCopyWritesPrevBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "conductor")
	content := []byte("#!/bin/sh\necho v1\n")
	if err := os.WriteFile(exe, content, 0o755); err != nil {
		t.Fatal(err)
	}
	saveRollbackCopy(exe, "v1.0.0")
	got, err := os.ReadFile(exe + prevBinarySuffix)
	if err != nil {
		t.Fatalf("expected a rollback copy at %s: %v", exe+prevBinarySuffix, err)
	}
	if string(got) != string(content) {
		t.Fatal("rollback copy content does not match the previous binary")
	}
}

// TestSaveRollbackCopyBestEffort: a backup failure (unreadable source) must
// not panic or block — it is best-effort, never allowed to stop an update
// that already passed preflight.
func TestSaveRollbackCopyBestEffort(t *testing.T) {
	saveRollbackCopy(filepath.Join(t.TempDir(), "does-not-exist"), "v1.0.0")
}

// The release check reports changed only when the newest published tag
// moves, and surfaces a lookup failure.
func TestReleaseCheckerReportsMovement(t *testing.T) {
	tags := []string{"v1.0.0", "v1.0.0", "v1.1.0"}
	i := 0
	rc := &releaseChecker{latest: func() (string, error) { i++; return tags[i-1], nil }}
	for n, want := range []bool{true, false, true} {
		tag, changed, err := rc.check()
		if err != nil || changed != want || tag != tags[n] {
			t.Fatalf("check %d: tag=%q changed=%v err=%v, want changed=%v", n, tag, changed, err, want)
		}
	}
	bad := &releaseChecker{latest: func() (string, error) { return "", errors.New("unreachable") }}
	if _, _, err := bad.check(); err == nil {
		t.Fatal("a failed lookup must be reported")
	}
}

func TestNewerRelease(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		changed bool
		running string
		want    bool
	}{
		{"nothing new", "", false, "v0.5.23", false},
		{"changed but same tag", "v0.5.23", true, "v0.5.23", false},
		{"changed and newer", "v0.5.24", true, "v0.5.23", true},
		{"changed but empty tag", "", true, "v0.5.23", false},
		{"never an older tag", "v0.60.0", true, "v0.61.0", false},
		{"a release candidate stays ahead of an older stable", "v0.60.0", true, "v0.61.0-rc.1", false},
		{"a release candidate moves to its release", "v0.61.0", true, "v0.61.0-rc.1", true},
		{"a dev build takes a published release", "v0.61.0", true, "dev", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newerRelease(c.tag, c.changed, c.running); got != c.want {
				t.Fatalf("newerRelease(%q,%v,%q) = %v, want %v", c.tag, c.changed, c.running, got, c.want)
			}
		})
	}
}

// The self-update preflight requires the release's plugins: a box never
// auto-updates into a release whose plugins it cannot find or fetch.
func TestPreflightValidateRequiresPlugins(t *testing.T) {
	bin := writeFakeConductor(t, `case "$*" in *--require-plugins*) exit 0;; esac; echo "preflight must pass --require-plugins" >&2; exit 1`)
	if err := preflightValidate(bin, "/any/config.yaml", true); err != nil {
		t.Fatalf("preflight did not pass --require-plugins: %v", err)
	}
}

// A forced update does not require the plugins (the operator's escape hatch
// for an unreachable source), but still proves the config loads.
func TestPreflightValidateForcedSkipsThePluginRequirement(t *testing.T) {
	bin := writeFakeConductor(t, `case "$*" in *--require-plugins*) echo "plugins required" >&2; exit 1;; esac; exit 0`)
	if err := preflightValidate(bin, "/any/config.yaml", false); err != nil {
		t.Fatalf("a forced preflight still required the plugins: %v", err)
	}
	if err := preflightValidate(bin, "/any/config.yaml", true); err == nil {
		t.Fatal("an unforced preflight must require them")
	}
}

// TestRefreshDepsRefusesOnConfigLoadError is a TEST GAP: refreshDeps had no
// direct coverage at all. A config whose `packs:` block resolves cleanly
// (ResolvePacks' own loose, packs-only unmarshal tolerates a stray
// top-level key its strict schema has since removed) but which config.Load
// itself refuses (the full strict schema) must make refreshDeps report "no
// change, nothing applied" — never a partial or crashed state — since the
// auto-update cycle must never let one malformed read anywhere in the
// pipeline propagate into applying something it can't actually re-load.
func TestRefreshDepsRefusesOnConfigLoadError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	// `integrations:` was removed with the legacy config schema (see
	// TestCmdValidate in cmd_more_test.go) — config.Load's strict
	// unmarshal refuses it, but it carries no `packs:` key at all, so
	// ResolvePacks' own loose, packs-only struct unmarshal ignores it
	// and returns an empty lockfile with no error.
	doc := "integrations:\n  - type: cron\n    name: chores\n"
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	packMoved, pluginRes, applied := refreshDeps(cfgPath)
	if applied {
		t.Fatal("a config.Load failure must never report applied=true")
	}
	if packMoved {
		t.Fatal("a config.Load failure must report packMoved=false")
	}
	if pluginRes != nil {
		t.Fatalf("a config.Load failure must return no plugin resolutions, got %+v", pluginRes)
	}
}

// autoUpdatePackRepo is a git repo publishing a minimal pack at tag v1.0.0;
// release adds the next tagged release with the given conductor floor.
type autoUpdatePackRepo struct {
	t    *testing.T
	repo string
}

func newAutoUpdatePackRepo(t *testing.T) *autoUpdatePackRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := &autoUpdatePackRepo{t: t, repo: t.TempDir()}
	r.git("init", "-b", "main")
	r.release("v1.0.0", ">=0.1")
	return r
}

func (r *autoUpdatePackRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.repo
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// release commits and tags a pack release; it returns the commit sha.
func (r *autoUpdatePackRepo) release(tag, floor string) string {
	r.t.Helper()
	manifest := `
pack:
  name: autopilot
  version: "` + strings.TrimPrefix(tag, "v") + `"
  requires:
    conductor: "` + floor + `"
workflows:
  flow:
    steps:
      - id: say
        type: agent
        name: say
        workspace: local
        prompt: release ` + tag + `
`
	if err := os.WriteFile(filepath.Join(r.repo, config.PackManifestFile), []byte(manifest), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", "-A")
	r.git("commit", "-m", tag)
	r.git("tag", tag)
	return r.git("rev-parse", "HEAD")
}

// setupAutoUpdatePack writes a config tracking the repo's pack with
// `~> 1.0`, resolves it as `conductor init` would, and returns the config
// path. The daemon runs as v0.66.0.
func setupAutoUpdatePack(t *testing.T, r *autoUpdatePackRepo) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	config.SetRuntimeVersion("v0.66.0")
	t.Cleanup(func() { config.SetRuntimeVersion("dev") })
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	doc := `
connectors:
  hook: { use: webhook }
packs:
  autopilot:
    source: git::file://` + r.repo + `
    version: "~> 1.0"
`
	if err := os.WriteFile(cfgPath, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(cfgPath); err != nil {
		t.Fatalf("ResolvePacks: %v", err)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("initial config must load: %v", err)
	}
	return cfgPath
}

// TestRefreshDepsLeavesNothingOnDiskWhenAPackUpdateFailsToLoad is #194: an
// auto-update tick resolving a pack release the config cannot load must
// leave the vendored tree and lockfile exactly as they were — not merely
// keep the running daemon's in-memory config — so an unrelated restart hours
// later boots the last state that loaded instead of coming up degraded.
func TestRefreshDepsLeavesNothingOnDiskWhenAPackUpdateFailsToLoad(t *testing.T) {
	r := newAutoUpdatePackRepo(t)
	cfgPath := setupAutoUpdatePack(t, r)
	dir := filepath.Dir(cfgPath)
	lockPath := filepath.Join(dir, config.LockfileName)
	vendored := filepath.Join(config.PackVendorDir(dir), "autopilot", config.PackManifestFile)
	lockBefore, _ := os.ReadFile(lockPath)
	manBefore, _ := os.ReadFile(vendored)
	lockInfo, _ := os.Stat(lockPath)

	// v1.1.0 satisfies `~> 1.0` but needs a newer daemon.
	r.release("v1.1.0", ">=99.0")
	for tick := 0; tick < 2; tick++ {
		packMoved, _, act := refreshDeps(cfgPath)
		if act || packMoved {
			t.Fatalf("tick %d: an update that does not load must not apply (act=%v packMoved=%v)", tick, act, packMoved)
		}
	}
	if lockAfter, _ := os.ReadFile(lockPath); string(lockAfter) != string(lockBefore) {
		t.Fatalf("the lockfile was rewritten by an update that never applied:\n%s", lockAfter)
	}
	if info, _ := os.Stat(lockPath); !info.ModTime().Equal(lockInfo.ModTime()) {
		t.Fatal("the lockfile was rewritten (mtime moved) by an update that never applied")
	}
	if manAfter, _ := os.ReadFile(vendored); string(manAfter) != string(manBefore) {
		t.Fatal("the vendored pack was replaced by an update that never applied")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".conductor", ".packs-stage-*")); len(left) != 0 {
		t.Fatalf("staging left %v behind", left)
	}
	// The next restart loads what was there before.
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("the on-disk config must still load after the refused update: %v", err)
	}
}

// TestRefreshDepsAppliesAPackUpdateThatLoads: the stage is swapped in when
// the update loads, so the restart that follows boots the new release.
func TestRefreshDepsAppliesAPackUpdateThatLoads(t *testing.T) {
	r := newAutoUpdatePackRepo(t)
	cfgPath := setupAutoUpdatePack(t, r)
	dir := filepath.Dir(cfgPath)

	want := r.release("v1.1.0", ">=0.1")
	packMoved, _, act := refreshDeps(cfgPath)
	if !act || !packMoved {
		t.Fatalf("a loadable pack update must apply (act=%v packMoved=%v)", act, packMoved)
	}
	lock, err := config.ReadLockfile(dir)
	if err != nil || lock == nil || len(lock.Packs) != 1 {
		t.Fatalf("lockfile after apply: %+v, %v", lock, err)
	}
	if lock.Packs[0].Resolved != want {
		t.Fatalf("lockfile pins %s, want v1.1.0's %s", lock.Packs[0].Resolved, want)
	}
	man, _ := os.ReadFile(filepath.Join(config.PackVendorDir(dir), "autopilot", config.PackManifestFile))
	if !strings.Contains(string(man), "release v1.1.0") {
		t.Fatal("the vendored pack was not updated to v1.1.0")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".conductor", ".packs-stage-*")); len(left) != 0 {
		t.Fatalf("staging left %v behind", left)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("the applied config must load: %v", err)
	}
}
