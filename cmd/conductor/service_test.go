package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServicePATHAndUnit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("PATH", "/custom/tool/bin:/usr/bin")

	p := servicePATH()
	parts := strings.Split(p, ":")
	if parts[0] != filepath.Join(tmp, ".local/bin") {
		t.Fatalf("~/.local/bin must be first: %q", p)
	}
	if !strings.Contains(p, "/custom/tool/bin") {
		t.Fatalf("install-time PATH entries should carry over: %q", p)
	}
	seen := map[string]bool{}
	for _, x := range parts {
		if seen[x] {
			t.Fatalf("PATH has a duplicate %q: %q", x, p)
		}
		seen[x] = true
	}

	// The rendered unit must set PATH so the service can find paseo/gh/etc.
	if serviceKind() == "" {
		t.Skip("no service manager on this OS")
	}
	_, content := unitPathAndContent()
	if !strings.Contains(content, "PATH="+p) && !strings.Contains(content, p) {
		t.Fatalf("unit should embed the service PATH:\n%s", content)
	}
}

// TestServiceName proves the service unit / launchd label name is always
// "conductor".
func TestServiceName(t *testing.T) {
	if got := serviceName(); got != "conductor" {
		t.Fatalf("serviceName() = %q, want conductor", got)
	}
}

// TestUnitPathAndContentUsesConductorName proves unitPathAndContent always
// targets the conductor-named unit path/label.
func TestUnitPathAndContentUsesConductorName(t *testing.T) {
	if serviceKind() == "" {
		t.Skip("no service manager on this OS")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	path, content := unitPathAndContent()
	switch serviceKind() {
	case "systemd":
		want := filepath.Join(tmp, ".config/systemd/user/conductor.service")
		if path != want {
			t.Fatalf("unit path = %q, want %q", path, want)
		}
	case "launchd":
		want := filepath.Join(tmp, "Library/LaunchAgents/sh.conductor.plist")
		if path != want {
			t.Fatalf("unit path = %q, want %q", path, want)
		}
		if !strings.Contains(content, "sh.conductor") {
			t.Fatalf("launchd Label should use the conductor name:\n%s", content)
		}
	}
}

// TestConfigDir proves configDir always resolves to ~/.config/conductor.
func TestConfigDir(t *testing.T) {
	t.Run("resolves to the conductor config dir", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		want := filepath.Join(tmp, ".config/conductor")
		if got := configDir(); got != want {
			t.Fatalf("configDir() = %q, want %q", got, want)
		}
	})

	t.Run("configPath default routes through configDir", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		def, _ := configPath(nil)
		want := filepath.Join(tmp, ".config/conductor/config.yaml")
		if def != want {
			t.Fatalf("configPath default = %q, want %q", def, want)
		}
	})
}

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
