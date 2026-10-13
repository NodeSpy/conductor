package confdir

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(Env, "")
	return h
}

func TestDefaultIsDotConfig(t *testing.T) {
	h := setup(t)
	want := filepath.Join(h, ".config", "conductor", "config.yaml")
	if got := File(); got != want {
		t.Fatalf("File() = %q, want %q", got, want)
	}
	if !IsDefault() {
		t.Fatal("no override set: IsDefault must be true")
	}
}

func TestXDGConfigHome(t *testing.T) {
	setup(t)
	x := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", x)
	if got, want := File(), filepath.Join(x, "conductor", "config.yaml"); got != want {
		t.Fatalf("File() = %q, want %q", got, want)
	}
	if IsDefault() {
		t.Fatal("XDG_CONFIG_HOME moved the config: IsDefault must be false")
	}
}

// The XDG spec says a relative XDG_CONFIG_HOME is invalid and ignored.
func TestRelativeXDGConfigHomeIgnored(t *testing.T) {
	h := setup(t)
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	if got, want := File(), filepath.Join(h, ".config", "conductor", "config.yaml"); got != want {
		t.Fatalf("File() = %q, want %q", got, want)
	}
}

func TestOverrideWinsOverXDG(t *testing.T) {
	setup(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := filepath.Join(t.TempDir(), "box.yaml")
	t.Setenv(Env, f)
	if got := File(); got != f {
		t.Fatalf("File() = %q, want the override %q", got, f)
	}
	if got := Dir(); got != filepath.Dir(f) {
		t.Fatalf("Dir() = %q, want %q", got, filepath.Dir(f))
	}
}

// An existing directory (or a trailing slash) means "config.yaml in there".
func TestOverrideDirectory(t *testing.T) {
	setup(t)
	d := t.TempDir()
	t.Setenv(Env, d)
	if got, want := File(), filepath.Join(d, "config.yaml"); got != want {
		t.Fatalf("File() = %q, want %q", got, want)
	}
	missing := filepath.Join(t.TempDir(), "not-yet") + "/"
	t.Setenv(Env, missing)
	if got, want := File(), filepath.Join(filepath.Clean(missing), "config.yaml"); got != want {
		t.Fatalf("trailing slash: File() = %q, want %q", got, want)
	}
}

func TestOverrideExpandsHomeAndRelative(t *testing.T) {
	h := setup(t)
	t.Setenv(Env, "~/cfg/conductor.yaml")
	if got, want := File(), filepath.Join(h, "cfg", "conductor.yaml"); got != want {
		t.Fatalf("~ expansion: File() = %q, want %q", got, want)
	}
	wd := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv(Env, "rel.yaml")
	got := File()
	if !filepath.IsAbs(got) || filepath.Base(got) != "rel.yaml" {
		t.Fatalf("relative override must resolve to an absolute path, got %q", got)
	}
}
