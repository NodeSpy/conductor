package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// ---- path resolution ----

func TestMigratePathResolution(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cases := map[string]string{
		"legacyBinPath":      filepath.Join(tmp, ".local/bin/paseo-conductor"),
		"newBinPath":         filepath.Join(tmp, ".local/bin/conductor"),
		"legacyConfigDirAbs": filepath.Join(tmp, ".config/paseo-conductor"),
		"newConfigDirAbs":    filepath.Join(tmp, ".config/conductor"),
		"legacyStateDirAbs":  filepath.Join(tmp, ".local/state/paseo-conductor"),
		"newStateDirAbs":     filepath.Join(tmp, ".local/state/conductor"),
	}
	got := map[string]string{
		"legacyBinPath":      legacyBinPath(),
		"newBinPath":         newBinPath(),
		"legacyConfigDirAbs": legacyConfigDirAbs(),
		"newConfigDirAbs":    newConfigDirAbs(),
		"legacyStateDirAbs":  legacyStateDirAbs(),
		"newStateDirAbs":     newStateDirAbs(),
	}
	for name, want := range cases {
		if got[name] != want {
			t.Errorf("%s() = %q, want %q", name, got[name], want)
		}
	}
}

// ---- detect ----

func TestDetectMigrationNothingToMigrate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	st := detectMigration()
	if st.needsMigration() {
		t.Fatalf("expected no migration needed on a clean HOME: %+v", st)
	}
	if st.legacyPresent() {
		t.Fatalf("expected no legacy install detected: %+v", st)
	}
}

func TestDetectMigrationLegacyBinaryPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	mustWriteFile(t, legacyBinPath(), "bin", 0o755)

	st := detectMigration()
	if !st.needsMigration() {
		t.Fatalf("expected migration needed with a legacy binary present: %+v", st)
	}
}

func TestDetectMigrationLegacyConfigOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.MkdirAll(legacyConfigDirAbs(), 0o755); err != nil {
		t.Fatal(err)
	}

	st := detectMigration()
	if !st.needsMigration() {
		t.Fatalf("expected migration needed with a legacy config dir present: %+v", st)
	}
}

func TestDetectMigrationAlreadyMigrated(t *testing.T) {
	if serviceKind() == "" {
		t.Skip("no service manager on this OS")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	mustWriteFile(t, legacyBinPath(), "bin", 0o755)
	mustWriteFile(t, newServiceUnitPath(), "unit", 0o644)

	st := detectMigration()
	if st.needsMigration() {
		t.Fatalf("expected already-migrated (conductor unit present) to be a no-op: %+v", st)
	}
	if !st.NewUnitExists {
		t.Fatalf("expected NewUnitExists=true: %+v", st)
	}
}

func mustWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// ---- recursive copy ----

func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("dst a.txt = %q, err %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); err != nil || string(got) != "world" {
		t.Fatalf("dst sub/b.txt = %q, err %v", got, err)
	}

	// Copy, never move: src must be untouched.
	if _, err := os.Stat(filepath.Join(src, "a.txt")); err != nil {
		t.Fatalf("source file missing after copy (copyTree must not move): %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "sub", "b.txt")); err != nil {
		t.Fatalf("source file missing after copy (copyTree must not move): %v", err)
	}
}

func TestCopyDirSkipExisting(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := copyDirSkipExisting(src, dst)
	if err != nil || !copied {
		t.Fatalf("first copy: copied=%v err=%v", copied, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "f")); string(got) != "v1" {
		t.Fatalf("dst content = %q", got)
	}

	// Mutate dst to prove a second run (dst already present) is skipped, not
	// merged/overwritten.
	if err := os.WriteFile(filepath.Join(dst, "f"), []byte("v2-local-edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	copied, err = copyDirSkipExisting(src, dst)
	if err != nil || copied {
		t.Fatalf("second copy should skip (dest exists): copied=%v err=%v", copied, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "f")); string(got) != "v2-local-edit" {
		t.Fatal("skip-existing must not overwrite a dest that already exists")
	}
}

func TestCopyDirSkipExistingNoSource(t *testing.T) {
	base := t.TempDir()
	copied, err := copyDirSkipExisting(filepath.Join(base, "nope"), filepath.Join(base, "dst"))
	if err != nil || copied {
		t.Fatalf("missing source should be a no-op: copied=%v err=%v", copied, err)
	}
}

// ---- the fail-safe orchestrator: order + rollback ----

// fakeServiceOps records the order operations are invoked in, so tests can
// assert the critical stop -> write-new -> start-new -> verify ->
// [remove-old | rollback] sequence.
type fakeServiceOps struct {
	calls        []string
	failWrite    bool
	failStart    bool
	failRemove   bool
	verifyResult bool
}

func (f *fakeServiceOps) stopOld() error {
	f.calls = append(f.calls, "stop-old")
	return nil
}

func (f *fakeServiceOps) writeNewUnit() error {
	f.calls = append(f.calls, "write-new")
	if f.failWrite {
		return errors.New("write failed")
	}
	return nil
}

func (f *fakeServiceOps) startNew() error {
	f.calls = append(f.calls, "start-new")
	if f.failStart {
		return errors.New("start failed")
	}
	return nil
}

func (f *fakeServiceOps) verifyActive(time.Duration) bool {
	f.calls = append(f.calls, "verify")
	return f.verifyResult
}

func (f *fakeServiceOps) removeOld() error {
	f.calls = append(f.calls, "remove-old")
	if f.failRemove {
		return errors.New("remove failed")
	}
	return nil
}

func (f *fakeServiceOps) rollback() error {
	f.calls = append(f.calls, "rollback")
	return nil
}

func TestMigrateServiceSuccessOrder(t *testing.T) {
	f := &fakeServiceOps{verifyResult: true}
	if err := migrateService(f, time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"stop-old", "write-new", "start-new", "verify", "remove-old"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %v, want %v", f.calls, want)
	}
}

func TestMigrateServiceRollbackOnVerifyFailure(t *testing.T) {
	f := &fakeServiceOps{verifyResult: false}
	err := migrateService(f, time.Millisecond)
	if err == nil {
		t.Fatal("expected an error when the new service never becomes active")
	}
	want := []string{"stop-old", "write-new", "start-new", "verify", "rollback"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %v, want %v", f.calls, want)
	}
	for _, c := range f.calls {
		if c == "remove-old" {
			t.Fatal("remove-old must NEVER run when verify fails — the old unit stays intact until the new service is confirmed active")
		}
	}
}

func TestMigrateServiceRollbackOnWriteFailure(t *testing.T) {
	f := &fakeServiceOps{failWrite: true}
	err := migrateService(f, time.Millisecond)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := []string{"stop-old", "write-new", "rollback"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %v, want %v", f.calls, want)
	}
}

func TestMigrateServiceRollbackOnStartFailure(t *testing.T) {
	f := &fakeServiceOps{failStart: true}
	err := migrateService(f, time.Millisecond)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := []string{"stop-old", "write-new", "start-new", "rollback"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %v, want %v", f.calls, want)
	}
}

func TestMigrateServiceRemoveOldFailureIsNonFatal(t *testing.T) {
	f := &fakeServiceOps{verifyResult: true, failRemove: true}
	if err := migrateService(f, time.Millisecond); err != nil {
		t.Fatalf("a failed old-unit cleanup after a confirmed-active new service must not fail the migration: %v", err)
	}
	want := []string{"stop-old", "write-new", "start-new", "verify", "remove-old"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %v, want %v", f.calls, want)
	}
}
