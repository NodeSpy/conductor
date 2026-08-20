package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestUnitContentAndSync(t *testing.T) {
	if serviceKind() == "" {
		t.Skip("no service manager on this OS")
	}
	// Redirect HOME so we never touch the real unit files.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path, content := unitPathAndContent()
	if path == "" || content == "" {
		t.Fatal("expected a unit path + content")
	}
	if !strings.Contains(content, "run") {
		t.Fatalf("unit should invoke the binary with `run`:\n%s", content)
	}

	// sync is a no-op when nothing is installed (must not create a service).
	changed, err := syncServiceUnit(false)
	if err != nil || changed {
		t.Fatalf("sync with no unit installed should be a no-op, changed=%v err=%v", changed, err)
	}

	// First write creates the unit.
	_, changed, err = writeUnitIfChanged()
	if err != nil || !changed {
		t.Fatalf("first write should change, changed=%v err=%v", changed, err)
	}
	// Idempotent: second write is unchanged.
	_, changed, _ = writeUnitIfChanged()
	if changed {
		t.Fatal("re-writing identical content should report no change")
	}
	// A drifted unit is detected as changed and rewritten.
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sync now sees an installed-but-stale unit → rewrites it.
	if runtime.GOOS == "linux" {
		// reloadService runs systemctl on linux; it may not exist in CI, but the
		// write path is what we assert. Ignore reload errors (best-effort).
	}
	changed, _ = syncServiceUnit(false)
	if !changed {
		t.Fatal("sync should rewrite a drifted unit")
	}
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Fatal("sync should restore the rendered content")
	}
}
