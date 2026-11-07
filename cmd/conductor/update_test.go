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
	"github.com/NodeSpy/conductor/internal/plugin"
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
	name string // the pack's name; "" = autopilot
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
	return r.releaseNeeding(tag, floor, "")
}

// releaseNeeding is release with a floor on a `widget` connector ("" for none).
func (r *autoUpdatePackRepo) releaseNeeding(tag, floor, widget string) string {
	r.t.Helper()
	name := r.name
	if name == "" {
		name = "autopilot"
	}
	connectors := ""
	if widget != "" {
		connectors = "\n    connectors: { widget: \"" + widget + "\" }"
	}
	manifest := `
pack:
  name: ` + name + `
  version: "` + strings.TrimPrefix(tag, "v") + `"
  requires:
    conductor: "` + floor + `"` + connectors + `
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

// withWidgetPlugin installs, as far as config's version gate can tell, the
// `widget` connector plugin at version, and makes the auto-update cycle's
// plugin refresh move it to next (when next is set).
func withWidgetPlugin(t *testing.T, version, next string) {
	t.Helper()
	config.SetConnectorVersions(map[string][]string{"connectors/widget": {version}})
	t.Cleanup(func() { config.SetConnectorVersions(nil) })
	prev := refreshPluginsStep
	t.Cleanup(func() { refreshPluginsStep = prev })
	refreshPluginsStep = func(*config.Config) (bool, []plugin.Resolution, error) {
		if next == "" || next == version {
			return false, nil, nil
		}
		version = next
		config.SetConnectorVersions(map[string][]string{"connectors/widget": {next}})
		return true, []plugin.Resolution{{Name: "widget", Action: plugin.ActionUpdated}}, nil
	}
}

// setupWidgetPacks writes a config with a widget connector and one pack
// instance per repo (instance name = the repo's pack name), each tracking
// `~> 1.0`, and resolves them as `conductor init` would.
func setupWidgetPacks(t *testing.T, repos ...*autoUpdatePackRepo) string {
	t.Helper()
	config.SetRuntimeVersion("v0.66.0")
	t.Cleanup(func() { config.SetRuntimeVersion("dev") })
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  w: { use: widget }\npacks:\n"
	for _, r := range repos {
		doc += "  " + r.name + ":\n    source: git::file://" + r.repo + "\n    version: \"~> 1.0\"\n"
	}
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

func newWidgetPackRepo(t *testing.T, name string) *autoUpdatePackRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := &autoUpdatePackRepo{t: t, repo: t.TempDir(), name: name}
	r.git("init", "-b", "main")
	r.releaseNeeding("v1.0.0", ">=0.1", ">=2.8.0")
	return r
}

func lockedRevision(t *testing.T, cfgPath, instance string) string {
	t.Helper()
	lock, err := config.ReadLockfile(filepath.Dir(cfgPath))
	if err != nil || lock == nil {
		t.Fatalf("lockfile: %+v, %v", lock, err)
	}
	for _, e := range lock.Packs {
		if e.Instance == instance {
			return e.Resolved
		}
	}
	t.Fatalf("no %s in the lockfile", instance)
	return ""
}

// The deadlock: a pack release needs a newer plugin, and both are released.
// Evaluated against the installed plugin the pack update never loads, so
// neither ever moved. The plugin refreshes first, and the pack is loaded
// against it: both move in one cycle.
func TestRefreshDepsTakesAPackReleaseWithThePluginItNeeds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := newWidgetPackRepo(t, "autopilot")
	withWidgetPlugin(t, "connectors/widget/v2.8.0", "connectors/widget/v2.9.0")
	cfgPath := setupWidgetPacks(t, r)

	want := r.releaseNeeding("v1.1.0", ">=0.1", ">=2.9.0")
	packMoved, res, act := refreshDeps(cfgPath)
	if !act || !packMoved || len(res) == 0 {
		t.Fatalf("the plugin and the pack must move together (act=%v packMoved=%v plugins=%v)", act, packMoved, res)
	}
	if got := lockedRevision(t, cfgPath, "autopilot"); got != want {
		t.Fatalf("lockfile pins %s, want v1.1.0's %s", got, want)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("the applied config must load: %v", err)
	}
}

// The fallback: the newest pack release needs a plugin release that doesn't
// exist yet. The plugin still moves, the pack takes its newest release that
// loads against it, and another pack is not held back by the first.
func TestRefreshDepsFallsBackToEachPacksNewestLoadableRelease(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a := newWidgetPackRepo(t, "autopilot")
	b := newWidgetPackRepo(t, "other")
	withWidgetPlugin(t, "connectors/widget/v2.8.0", "connectors/widget/v2.9.0")
	cfgPath := setupWidgetPacks(t, a, b)

	loadable := a.releaseNeeding("v1.1.0", ">=0.1", ">=2.9.0")
	a.releaseNeeding("v1.2.0", ">=0.1", ">=3.0.0") // no such plugin release yet
	otherNew := b.releaseNeeding("v1.1.0", ">=0.1", ">=2.8.0")

	packMoved, res, act := refreshDeps(cfgPath)
	if !act || !packMoved || len(res) == 0 {
		t.Fatalf("the plugin and the loadable packs must move (act=%v packMoved=%v plugins=%v)", act, packMoved, res)
	}
	if got := lockedRevision(t, cfgPath, "autopilot"); got != loadable {
		t.Fatalf("autopilot pins %s, want its newest loadable release v1.1.0 (%s)", got, loadable)
	}
	if got := lockedRevision(t, cfgPath, "other"); got != otherNew {
		t.Fatalf("other pins %s, want its v1.1.0 (%s): a refused pack must not hold it back", got, otherNew)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("the applied config must load: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(cfgPath), ".conductor", ".packs-stage-*")); len(left) != 0 {
		t.Fatalf("staging left %v behind", left)
	}

	// The next cycle: nothing newer loads, nothing moves.
	if packMoved, _, act := refreshDeps(cfgPath); act || packMoved {
		t.Fatalf("a second cycle must be a no-op (act=%v packMoved=%v)", act, packMoved)
	}
}

// No pack release loads, but the plugin does: the plugin still moves, and the
// packs stay where they are.
func TestRefreshDepsMovesThePluginWhenNoPackReleaseLoads(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := newWidgetPackRepo(t, "autopilot")
	withWidgetPlugin(t, "connectors/widget/v2.8.0", "connectors/widget/v2.9.0")
	cfgPath := setupWidgetPacks(t, r)
	current := lockedRevision(t, cfgPath, "autopilot")

	r.releaseNeeding("v1.1.0", ">=0.1", ">=3.0.0")
	packMoved, res, act := refreshDeps(cfgPath)
	if !act || packMoved || len(res) == 0 {
		t.Fatalf("the plugin alone must move (act=%v packMoved=%v plugins=%v)", act, packMoved, res)
	}
	if got := lockedRevision(t, cfgPath, "autopilot"); got != current {
		t.Fatalf("autopilot moved to %s; it must stay at %s", got, current)
	}
}

// Logs & audit's Daemon log tab reads a line's source and level from its raw
// text (logf writes plain lines; logring keeps no structure of its own —
// internal/logring). A line like "WARNING: browser: ..." must show "browser"
// as its source, with the level (warn) carried only in the row's colour —
// not the level word repeated into the source column.
func TestDaemonLogSourceSkipsALeadingLevelWord(t *testing.T) {
	cases := []struct{ line, source, level string }{
		{"WARNING: browser: no browser is installed yet", "browser", "warn"},
		{"WARNING: sandbox.agents: off — agents run UNSANDBOXED", "sandbox.agents", "warn"},
		{"ERROR: plugins: fetch failed", "plugins", "err"},
		{"gh: sweep 14 repos · 0 missed · next in 30s", "gh", "info"},
		{"a line with no component prefix at all", "conductor", "info"},
	}
	for _, c := range cases {
		if got := daemonLogSource(c.line); got != c.source {
			t.Errorf("daemonLogSource(%q) = %q, want %q", c.line, got, c.source)
		}
		if got := daemonLogLevel(c.line); got != c.level {
			t.Errorf("daemonLogLevel(%q) = %q, want %q", c.line, got, c.level)
		}
	}
}
