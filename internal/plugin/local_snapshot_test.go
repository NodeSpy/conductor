package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
