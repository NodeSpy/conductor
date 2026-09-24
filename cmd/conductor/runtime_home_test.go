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

// The rungs that depend on this box's env and filesystem are unit-tested with
// an injected clock/prober in internal/paseover; what matters HERE is that the
// config fields reach the ladder and that the primary dispatcher gets its
// answer. See paseover.Resolve for the ladder itself.
func TestResolvePaseoEndpoint(t *testing.T) {
	// Explicit home: on the default paseo runtime wins over the ambient env.
	t.Run("explicit home", func(t *testing.T) {
		t.Setenv("PASEO_HOME", "/env/home")
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{
			"paseo": paseoCC("/srv/paseo", "", true),
		}}
		if got := resolvePaseoEndpoint(cfg).Home(); got != "/srv/paseo" {
			t.Fatalf("got %q, want /srv/paseo", got)
		}
	})

	// An explicit server: wins over an explicit home: — it is the same choice,
	// made directly instead of through a config.json lookup.
	t.Run("server beats home", func(t *testing.T) {
		cc := paseoCC("/srv/paseo", "", true)
		cc.Server = "127.0.0.1:6767"
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{"paseo": cc}}
		ep := resolvePaseoEndpoint(cfg)
		if ep.Server() != "127.0.0.1:6767" {
			t.Fatalf("server = %q, want 127.0.0.1:6767", ep.Server())
		}
		if ep.Home() != "" {
			t.Fatalf("a server endpoint must not also carry a home, got %q", ep.Home())
		}
	})

	// No config home, PASEO_HOME set → falls back to the env.
	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("PASEO_HOME", "/env/home")
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{
			"paseo": paseoCC("", "", true),
		}}
		if got := resolvePaseoEndpoint(cfg).Home(); got != "/env/home" {
			t.Fatalf("got %q, want /env/home", got)
		}
	})

	// With nothing declared the ladder must still name a daemon. Empty here is
	// the old behavior that made `paseo run` fail with MISSING_PROVIDER while a
	// perfectly good daemon was answering on the default port.
	t.Run("nothing declared still resolves", func(t *testing.T) {
		os.Unsetenv("PASEO_HOME")
		cfg := &config.Config{Controllers: map[string]config.ControllerConfig{
			"paseo": paseoCC("", "", true),
		}}
		ep := resolvePaseoEndpoint(cfg)
		if len(ep.Args) == 0 {
			t.Fatal("local runtime resolved to nothing; the ladder must reach a default")
		}
		if ep.Source == "" {
			t.Error("endpoint carries no source; the boot log needs to name the rung")
		}
	})

	// A REMOTE paseo runtime does not inherit this box's PASEO_HOME, ~/.paseo,
	// or listening ports — none of them describe the far box.
	t.Run("remote ignores local defaults", func(t *testing.T) {
		t.Setenv("PASEO_HOME", "/env/home")
		ep := paseoEndpointFor(paseoRuntimeDef{Name: "gpu", Host: "build-box"})
		if len(ep.Args) != 0 {
			t.Fatalf("remote runtime got %v, want no selector", ep.Args)
		}
	})

	// …but an explicit home: on a remote runtime still applies: it names a path
	// on THAT box, which is the one thing we can know about it.
	t.Run("remote honors explicit home", func(t *testing.T) {
		ep := paseoEndpointFor(paseoRuntimeDef{Name: "gpu", Host: "build-box", Home: "/far/home"})
		if got := ep.Home(); got != "/far/home" {
			t.Fatalf("got %q, want /far/home", got)
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
