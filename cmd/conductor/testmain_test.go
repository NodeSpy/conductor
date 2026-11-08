package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain isolates the plugin/pack install-state directory for the WHOLE
// cmd/conductor test package. Several tests drive real CLI commands — `conductor
// init`, `pack update`, `plugin update` — which RECONCILE install state to the
// config they are handed and prune records that config does not reference. Run
// against a developer's or CI runner's actual ~/.local/state/conductor, a test
// whose fixture config references (say) no `js` engine would delete the real js
// plugin from the live manifest — silently breaking the running daemon on its
// next restart. That is exactly the recurring "js not installed" wipe.
//
// config.StateDir() resolves override → XDG_STATE_HOME → ~/.local/state, so
// pointing XDG_STATE_HOME at a throwaway dir here guarantees no test in this
// package can touch the real manifest, regardless of whether the individual test
// remembered to pass --state-dir. Tests that set their own state dir still win.
//
// HOME itself is a temp dir too (testhome), and PASEO_HOME is dropped: the
// daemon's start-up paseo merge and `conductor paseo setup` fall back to
// $PASEO_HOME, then ~/.paseo — a developer's live paseo config. A mutant
// that lost a runtime's home: once wrote conductor's entries there.
func TestMain(m *testing.M) {
	// The plugin jail's entry is conductor's own binary (`conductor
	// sandbox-net …`), which in this package is the test binary: act as it.
	if len(os.Args) > 1 && os.Args[1] == "sandbox-net" {
		os.Exit(runSandboxNet(os.Args[2:]))
	}
	// checkConfigConnectors re-runs this binary as `config migrate-check`;
	// under go test that is the test binary, so serve it here.
	if len(os.Args) > 2 && os.Args[1] == "config" && os.Args[2] == "migrate-check" {
		if err := cmdConfigMigrateCheck(os.Args[3:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	capAgentSlice = func(string, string, string) error { return nil } // never the real user manager
	// The jailed plugins' fixtures (a configured log file) live in the
	// temp dir, which the jail otherwise grants nothing inside.
	plugin.GrantInsideTempForTests()
	root, err := testhome.Isolate()
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	os.Unsetenv("PASEO_HOME")
	tmp, err := os.MkdirTemp("", "conductor-cmd-teststate")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", tmp)
	// The same for the config directory: confdir resolves CONDUCTOR_CONFIG →
	// XDG_CONFIG_HOME → ~/.config. A CI runner sets XDG_CONFIG_HOME (GitHub's
	// does), so a test that points HOME at a temp dir would otherwise still
	// resolve the runner's real config dir. Clearing both makes HOME decide,
	// as every such test expects; tests that set them still win.
	os.Unsetenv("CONDUCTOR_CONFIG")
	os.Unsetenv("XDG_CONFIG_HOME")
	code := m.Run()
	os.RemoveAll(tmp)
	os.RemoveAll(root)
	os.Exit(code)
}
