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

// TestServiceName proves the fleet-safe back-compat behavior: a box with the
// legacy paseo-conductor unit already installed keeps every systemctl/launchctl
// operation targeting "paseo-conductor" (so an existing daemon stays
// manageable across an upgrade), while a box with no unit installed (a fresh
// install) gets the new default "conductor".
func TestServiceName(t *testing.T) {
	if serviceKind() == "" {
		t.Skip("no service manager on this OS")
	}

	t.Run("existing legacy unit wins", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		writeLegacyUnit(t, tmp)
		if got := serviceName(); got != "paseo-conductor" {
			t.Fatalf("serviceName() = %q, want paseo-conductor", got)
		}
	})

	t.Run("no unit installed -> new default", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		if got := serviceName(); got != "conductor" {
			t.Fatalf("serviceName() = %q, want conductor", got)
		}
	})

	t.Run("unitPathAndContent targets the legacy unit path/label when present", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		writeLegacyUnit(t, tmp)
		path, content := unitPathAndContent()
		switch serviceKind() {
		case "systemd":
			want := filepath.Join(tmp, ".config/systemd/user/paseo-conductor.service")
			if path != want {
				t.Fatalf("unit path = %q, want %q", path, want)
			}
		case "launchd":
			want := filepath.Join(tmp, "Library/LaunchAgents/sh.paseo-conductor.plist")
			if path != want {
				t.Fatalf("unit path = %q, want %q", path, want)
			}
			if !strings.Contains(content, "sh.paseo-conductor") {
				t.Fatalf("launchd Label should keep the legacy name:\n%s", content)
			}
		}
	})

	t.Run("unitPathAndContent uses the new name on a fresh install", func(t *testing.T) {
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
				t.Fatalf("launchd Label should use the new name:\n%s", content)
			}
		}
	})
}

// writeLegacyUnit creates an empty legacy paseo-conductor unit file under a
// temp HOME so detection sees an "already installed" fleet box.
func writeLegacyUnit(t *testing.T, home string) {
	t.Helper()
	var p string
	switch serviceKind() {
	case "systemd":
		p = filepath.Join(home, ".config/systemd/user/paseo-conductor.service")
	case "launchd":
		p = filepath.Join(home, "Library/LaunchAgents/sh.paseo-conductor.plist")
	default:
		t.Skip("no service manager on this OS")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestConfigDir proves the fleet-safe back-compat behavior: a box with an
// existing ~/.config/paseo-conductor keeps resolving to it, while a fresh
// install (neither directory present) gets the new default.
func TestConfigDir(t *testing.T) {
	t.Run("existing legacy dir wins", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		legacy := filepath.Join(tmp, ".config/paseo-conductor")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := configDir(); got != legacy {
			t.Fatalf("configDir() = %q, want legacy %q", got, legacy)
		}
	})

	t.Run("existing new dir wins over legacy", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		legacy := filepath.Join(tmp, ".config/paseo-conductor")
		fresh := filepath.Join(tmp, ".config/conductor")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(fresh, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := configDir(); got != fresh {
			t.Fatalf("configDir() = %q, want new %q (an existing new dir takes priority)", got, fresh)
		}
	})

	t.Run("neither exists -> new default", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		want := filepath.Join(tmp, ".config/conductor")
		if got := configDir(); got != want {
			t.Fatalf("configDir() = %q, want new default %q", got, want)
		}
	})

	t.Run("configPath default routes through configDir", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		legacy := filepath.Join(tmp, ".config/paseo-conductor")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		def, _ := configPath(nil)
		want := filepath.Join(legacy, "config.yaml")
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
