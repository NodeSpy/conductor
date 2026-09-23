package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func paseoCC(home, host string, def bool) config.ControllerConfig {
	return config.ControllerConfig{Type: "paseo", Home: home, Host: host, Default: def}
}

func TestResolvePaseoHome(t *testing.T) {
	// Explicit home: on the default paseo runtime wins.
	t.Run("explicit home", func(t *testing.T) {
		t.Setenv("PASEO_HOME", "/env/home")
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{
			"paseo": paseoCC("/srv/paseo", "", true),
		}}
		if got := resolvePaseoHome(cfg); got != "/srv/paseo" {
			t.Fatalf("got %q, want /srv/paseo", got)
		}
	})

	// No config home, PASEO_HOME env → falls back to env.
	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("PASEO_HOME", "/env/home")
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{
			"paseo": paseoCC("", "", true),
		}}
		if got := resolvePaseoHome(cfg); got != "/env/home" {
			t.Fatalf("got %q, want /env/home", got)
		}
	})

	// No config, no env → empty (paseo's own default).
	t.Run("unset", func(t *testing.T) {
		os.Unsetenv("PASEO_HOME")
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{
			"paseo": paseoCC("", "", true),
		}}
		if got := resolvePaseoHome(cfg); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	// A REMOTE paseo runtime does not inherit this box's PASEO_HOME.
	t.Run("remote ignores env", func(t *testing.T) {
		t.Setenv("PASEO_HOME", "/env/home")
		def := paseoRuntimeDef{Name: "gpu", Host: "build-box"}
		if got := paseoHomeFor(def); got != "" {
			t.Fatalf("remote runtime got %q, want empty (no env inheritance)", got)
		}
	})
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandTilde("~/Projects/paseo-home"); got != filepath.Join(home, "Projects/paseo-home") {
		t.Fatalf("got %q", got)
	}
	if got := expandTilde("~"); got != home {
		t.Fatalf("bare ~ got %q, want %q", got, home)
	}
	if got := expandTilde("/abs/path"); got != "/abs/path" {
		t.Fatalf("absolute path should be unchanged, got %q", got)
	}
}
