package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// The local-build TOCTOU fix (docs/design/plugin-contract.md): a `use: ./path`
// reference is hashed once per resolution and copied to a content-addressed,
// private snapshot — SpecFromRef/snapshotLocal — so every subsequent
// verify()+exec of THIS resolution (every probe, the live spawn, every
// crash-respawn) sees the same immutable bytes regardless of what the
// operator does to the mutable source path afterward.

// TestLocalBuildSnapshotPinsContentAcrossRebuild is the headline guarantee:
// rebuilding the source AFTER a resolution never changes what that
// resolution's Spec runs, and a NEW resolution (what a reload/restart does)
// picks up the rebuild.
func TestLocalBuildSnapshotPinsContentAcrossRebuild(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	src := filepath.Join(t.TempDir(), "conductor-widget")
	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := refFor(t, config.UseKindConnector, src)

	spec1 := SpecFromRef(ref, "", Installed{}, false)
	if spec1.SnapshotErr != nil {
		t.Fatalf("snapshot: %v", spec1.SnapshotErr)
	}
	if spec1.Sha256 == "" {
		t.Fatal("expected a recorded sha for the snapshot — verify() has nothing to pin against otherwise")
	}
	if spec1.BinPath == src {
		t.Fatal("the resolved Spec must point at the SNAPSHOT, not the mutable source path")
	}
	data1, err := os.ReadFile(spec1.BinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data1) != "v1" {
		t.Fatalf("snapshot content = %q, want v1", data1)
	}
	if _, err := verify(spec1); err != nil {
		t.Fatalf("a freshly snapshotted local build must verify against its own recorded sha: %v", err)
	}

	// Rebuild the SOURCE after resolution — a different length so a stale
	// read could never pass for a truncated copy by accident.
	if err := os.WriteFile(src, []byte("v2-is-longer-than-v1"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The ALREADY-RESOLVED spec must still run what IT resolved: a live
	// Client never re-reads the source path mid-life, and verify() must still
	// see the pinned snapshot bytes, not the rebuilt source.
	if _, err := verify(spec1); err != nil {
		t.Fatalf("an already-resolved spec must still verify after the source rebuilds: %v", err)
	}
	data1b, err := os.ReadFile(spec1.BinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data1b) != "v1" {
		t.Fatalf("snapshot mutated after the source rebuilt: %q", data1b)
	}

	// A NEW resolution — what a reload or restart performs — picks the
	// rebuild up, as a different sha pointing at a different snapshot.
	spec2 := SpecFromRef(ref, "", Installed{}, false)
	if spec2.SnapshotErr != nil {
		t.Fatalf("snapshot: %v", spec2.SnapshotErr)
	}
	if spec2.Sha256 == spec1.Sha256 {
		t.Fatal("a rebuilt source must resolve to a different sha on the NEXT resolution")
	}
	data2, err := os.ReadFile(spec2.BinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data2) != "v2-is-longer-than-v1" {
		t.Fatalf("new resolution's snapshot content = %q", data2)
	}

	// The OLD snapshot is untouched by the new resolution — a process still
	// running from spec1 (mid-reload, say) keeps seeing "v1".
	data1c, err := os.ReadFile(spec1.BinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data1c) != "v1" {
		t.Fatal("an earlier resolution's snapshot must remain untouched by a later one")
	}
}

// TestLocalBuildSnapshotIsIdempotent proves the cheap path: resolving the SAME
// unchanged source again (e.g. a `conductor plugin list` after boot) reuses
// the existing snapshot rather than making a new copy every time.
func TestLocalBuildSnapshotIsIdempotent(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	src := filepath.Join(t.TempDir(), "conductor-widget")
	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := refFor(t, config.UseKindConnector, src)

	spec1 := SpecFromRef(ref, "", Installed{}, false)
	if spec1.SnapshotErr != nil {
		t.Fatal(spec1.SnapshotErr)
	}
	spec2 := SpecFromRef(ref, "", Installed{}, false)
	if spec2.SnapshotErr != nil {
		t.Fatal(spec2.SnapshotErr)
	}
	if spec1.BinPath != spec2.BinPath || spec1.Sha256 != spec2.Sha256 {
		t.Fatalf("resolving an unchanged source twice must agree: %+v vs %+v", spec1, spec2)
	}
}

// TestLocalBuildSnapshotPermissions proves the snapshot directory is private
// (0700) and the snapshotted binary is read+execute only, no write for anyone
// (0500) — nothing, not even the daemon's own later code, can mutate what a
// resolution pinned.
func TestLocalBuildSnapshotPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission bits")
	}
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	src := filepath.Join(t.TempDir(), "conductor-widget")
	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := refFor(t, config.UseKindConnector, src)
	spec := SpecFromRef(ref, "", Installed{}, false)
	if spec.SnapshotErr != nil {
		t.Fatal(spec.SnapshotErr)
	}

	fi, err := os.Stat(spec.BinPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o500 {
		t.Fatalf("snapshot file perms = %v, want 0500", fi.Mode().Perm())
	}
	dirFi, err := os.Stat(filepath.Dir(spec.BinPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirFi.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot dir perms = %v, want 0700", dirFi.Mode().Perm())
	}
}

// TestGCLocalSnapshots proves GC removes only the snapshots NOT in the keep
// set, leaving a live one untouched.
func TestGCLocalSnapshots(t *testing.T) {
	root := t.TempDir()
	const keepSha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const staleSha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, sha := range []string{keepSha, staleSha} {
		dir := filepath.Join(root, sha)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "widget"), []byte("x"), 0o500); err != nil {
			t.Fatal(err)
		}
	}

	removed, errs := GCLocalSnapshots(root, map[string]bool{keepSha: true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(removed) != 1 || removed[0] != staleSha {
		t.Fatalf("removed = %v, want [%s]", removed, staleSha)
	}
	if _, err := os.Stat(filepath.Join(root, keepSha)); err != nil {
		t.Fatalf("a kept snapshot must survive GC: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, staleSha)); !os.IsNotExist(err) {
		t.Fatalf("a stale snapshot must be removed by GC, stat err = %v", err)
	}
}

// TestGCLocalSnapshotsMissingRootIsNoop proves GC against a root that does not
// exist yet (no local plugin ever resolved) is a quiet no-op, not an error —
// the common case on a box with no local plugins at all.
func TestGCLocalSnapshotsMissingRootIsNoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	removed, errs := GCLocalSnapshots(root, nil)
	if len(removed) != 0 || len(errs) != 0 {
		t.Fatalf("want a quiet no-op, got removed=%v errs=%v", removed, errs)
	}
}

// TestManagerLocalSnapshotShas proves the Manager-level GC-keep-set helper
// reports exactly the sha a resolved local plugin is pinned to.
func TestManagerLocalSnapshotShas(t *testing.T) {
	mgr, key := managerWithLocalPlugin(t, false)
	defer mgr.Close()
	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	shas := mgr.LocalSnapshotShas()
	if !shas[spec.Sha256] {
		t.Fatalf("expected %s's sha %s in LocalSnapshotShas, got %v", key, spec.Sha256, shas)
	}
}

// TestSnapshotLocalRefusesSymlinkDestination proves finding 3(a): a symlink
// planted at the content-addressed destination (another process, or an
// attacker with write access to the state dir before it was secured) is
// never reused or written through — snapshotLocal refuses outright rather
// than silently executing whatever the symlink points at.
func TestSnapshotLocalRefusesSymlinkDestination(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	src := filepath.Join(t.TempDir(), "conductor-widget")
	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := refFor(t, config.UseKindConnector, src)

	// Resolve once to learn the sha/dir this content hashes to, then replace
	// the installed snapshot FILE with a symlink pointing elsewhere.
	spec1 := SpecFromRef(ref, "", Installed{}, false)
	if spec1.SnapshotErr != nil {
		t.Fatalf("snapshot: %v", spec1.SnapshotErr)
	}
	elsewhere := filepath.Join(t.TempDir(), "evil")
	if err := os.WriteFile(elsewhere, []byte("evil"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(spec1.BinPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, spec1.BinPath); err != nil {
		t.Fatal(err)
	}

	// Resolving the SAME source again must hash to the same destination
	// path — and must now REFUSE it, not silently return the symlink path
	// for a caller to go on to verify()+exec.
	spec2 := SpecFromRef(ref, "", Installed{}, false)
	if spec2.SnapshotErr == nil {
		t.Fatalf("a symlink at the snapshot destination must be refused, got a clean Spec: %+v", spec2)
	}
	if !strings.Contains(spec2.SnapshotErr.Error(), "symlink") && !strings.Contains(spec2.SnapshotErr.Error(), "regular file") {
		t.Fatalf("refusal should call out the symlink, got: %v", spec2.SnapshotErr)
	}
}

// TestSnapshotLocalRefusesSymlinkDir proves the directory half: a symlink
// planted at the content-addressed DIRECTORY path is refused the same way,
// and is never chmod'ed or written through.
func TestSnapshotLocalRefusesSymlinkDir(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	src := filepath.Join(t.TempDir(), "conductor-widget")
	data := []byte("v1")
	if err := os.WriteFile(src, data, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	root := LocalSnapshotRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhereDir := filepath.Join(t.TempDir(), "evil-dir")
	if err := os.MkdirAll(elsewhereDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhereDir, filepath.Join(root, sha)); err != nil {
		t.Fatal(err)
	}

	ref := refFor(t, config.UseKindConnector, src)
	spec := SpecFromRef(ref, "", Installed{}, false)
	if spec.SnapshotErr == nil {
		t.Fatalf("a symlinked snapshot directory must be refused, got a clean Spec: %+v", spec)
	}
	if !strings.Contains(spec.SnapshotErr.Error(), "symlink") {
		t.Fatalf("refusal should call out the symlink, got: %v", spec.SnapshotErr)
	}
	// And the evil directory's permissions must be untouched — never
	// chmod'ed through the symlink.
	fi, err := os.Stat(elsewhereDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("a symlinked-to directory must never be chmod'ed through the symlink, got perm %v", fi.Mode().Perm())
	}
}

// TestGCLocalSnapshotsOldRespectsGraceWindow proves finding 3(b): a snapshot
// touched within the grace window survives GCLocalSnapshotsOld even though
// it is not in any `keep` set this process knows about — the cross-daemon
// safety net. One untouched well past the window is removed.
func TestGCLocalSnapshotsOldRespectsGraceWindow(t *testing.T) {
	root := t.TempDir()
	const freshSha = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const staleSha = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	for _, sha := range []string{freshSha, staleSha} {
		dir := filepath.Join(root, sha)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "widget"), []byte("x"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	// freshSha was "just touched" (now); staleSha was last touched 30 days
	// ago — well past any reasonable grace window.
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, staleSha), old, old); err != nil {
		t.Fatal(err)
	}

	removed, errs := GCLocalSnapshotsOld(root, DefaultLocalSnapshotGrace)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(removed) != 1 || removed[0] != staleSha {
		t.Fatalf("removed = %v, want [%s]", removed, staleSha)
	}
	if _, err := os.Stat(filepath.Join(root, freshSha)); err != nil {
		t.Fatalf("a recently-touched snapshot must survive the grace-period GC: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, staleSha)); !os.IsNotExist(err) {
		t.Fatalf("an untouched-past-grace snapshot must be removed, stat err = %v", err)
	}
}

// TestSnapshotLocalTouchesOnEveryResolve proves the OTHER half of GC
// coordination: resolving the SAME unchanged source again (the idempotent
// reuse path) still refreshes the snapshot dir's mtime — otherwise a
// snapshot that is resolved over and over but never freshly WRITTEN would
// look abandoned to GCLocalSnapshotsOld and get removed out from under a
// live process.
func TestSnapshotLocalTouchesOnEveryResolve(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	src := filepath.Join(t.TempDir(), "conductor-widget")
	if err := os.WriteFile(src, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := refFor(t, config.UseKindConnector, src)

	spec1 := SpecFromRef(ref, "", Installed{}, false)
	if spec1.SnapshotErr != nil {
		t.Fatal(spec1.SnapshotErr)
	}
	dir := filepath.Dir(spec1.BinPath)
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	// Re-resolve the SAME unchanged source (the idempotent/reuse path).
	spec2 := SpecFromRef(ref, "", Installed{}, false)
	if spec2.SnapshotErr != nil {
		t.Fatal(spec2.SnapshotErr)
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("resolving an unchanged snapshot again must refresh its mtime (GC coordination), got mtime %v", fi.ModTime())
	}
}

// TestTouchSnapshotUsedRefreshesMtime is a direct unit proof of the
// primitive touchSnapshotUsed (Client.ensureLocked calls it on every spawn
// of a local snapshot, local_snapshot_test.go's
// TestSnapshotLocalTouchesOnEveryResolve covers the resolve call site):
// given a directory whose mtime was pushed back well past any grace window,
// touchSnapshotUsed brings it back to "now".
func TestTouchSnapshotUsedRefreshesMtime(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	touchSnapshotUsed(dir)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("touchSnapshotUsed did not refresh mtime, got %v", fi.ModTime())
	}
}

// TestClientStartTouchesLocalSnapshotOnSpawn proves the SPAWN half of GC
// coordination end to end: starting a real subprocess from a local snapshot
// refreshes the snapshot dir's mtime too, not just a resolve — a
// crash-respawn (no fresh SpecFromRef call; the live Client just re-dials
// its already-resolved Spec) must still keep the snapshot looking "in use"
// to GCLocalSnapshotsOld.
//
// SpecFromRef never populates Manifest for a LOCAL reference (only a remote
// install record carries one), so confineToManifest's commandPathDir always
// sees a zero Manifest and — since `m.Spawns` is false — always proceeds to
// create/remove a `.path` subdirectory under the snapshot dir on every
// spawn. That would ALSO bump the snapshot dir's own mtime as a side effect
// and mask a regression in the explicit touchSnapshotUsed call this test
// exists to check. Forcing Manifest.Spawns true (as if a remote-install
// record had recorded "spawns, no named commands") takes commandPathDir's
// documented early-return branch instead ("a plugin declaring 'I spawn
// things I am not naming' gets no PATH rewrite at all" — manifest.go),
// isolating the assertion to the explicit touch alone.
func TestClientStartTouchesLocalSnapshotOnSpawn(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	bin, _ := buildExamplePlugin(t)
	ref := refFor(t, config.UseKindConnector, bin)
	spec := SpecFromRef(ref, "", Installed{}, false)
	if spec.SnapshotErr != nil {
		t.Fatal(spec.SnapshotErr)
	}
	spec.Manifest.Spawns = true // see comment above

	dir := filepath.Dir(spec.BinPath)
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}

	c := NewClient(spec, Deps{})
	defer c.Close()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Confirm the confound really is absent: no `.path` dir was created.
	if _, err := os.Stat(filepath.Join(dir, ".path")); err == nil {
		t.Fatal("test setup invalid: commandPathDir created a .path dir, which would confound the mtime assertion below")
	}

	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("spawning from a local snapshot must refresh its mtime (GC coordination), got mtime %v", fi.ModTime())
	}
}

// TestGCLocalSnapshotsSafelyNeedsBothSignals proves finding 3(b)'s combined
// GC (what cmd/conductor's boot GC actually runs): a snapshot is removed
// ONLY when it is both OUTSIDE the keep set AND past the grace window.
// Three cases: in keep + fresh (survives, trivially); outside keep but
// FRESH — e.g. a sibling daemon's own live snapshot, invisible to this
// process's keep set — must survive; outside keep AND stale is the only one
// actually removed.
func TestGCLocalSnapshotsSafelyNeedsBothSignals(t *testing.T) {
	root := t.TempDir()
	const keptSha = "1111111111111111111111111111111111111111111111111111111111111a"
	const freshUnknownSha = "1111111111111111111111111111111111111111111111111111111111111b"
	const staleUnknownSha = "1111111111111111111111111111111111111111111111111111111111111c"
	for _, sha := range []string{keptSha, freshUnknownSha, staleUnknownSha} {
		dir := filepath.Join(root, sha)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "widget"), []byte("x"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, staleUnknownSha), old, old); err != nil {
		t.Fatal(err)
	}
	// keptSha is in keep but also push its mtime back — keep alone must be
	// enough to save it, regardless of mtime.
	if err := os.Chtimes(filepath.Join(root, keptSha), old, old); err != nil {
		t.Fatal(err)
	}

	removed, errs := GCLocalSnapshotsSafely(root, map[string]bool{keptSha: true}, DefaultLocalSnapshotGrace)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(removed) != 1 || removed[0] != staleUnknownSha {
		t.Fatalf("removed = %v, want only [%s]", removed, staleUnknownSha)
	}
	for _, sha := range []string{keptSha, freshUnknownSha} {
		if _, err := os.Stat(filepath.Join(root, sha)); err != nil {
			t.Fatalf("%s must survive (kept, or outside keep but fresh): %v", sha, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, staleUnknownSha)); !os.IsNotExist(err) {
		t.Fatalf("%s (outside keep AND stale) must be removed, stat err = %v", staleUnknownSha, err)
	}
}
