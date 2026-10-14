package main

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/testhome"
)

// TestMain isolates HOME, and with it the plugin/pack install-state and config
// directories, for the WHOLE cmd/conductor test package. Several tests drive
// real CLI commands — `conductor init`, `pack update`, `plugin update` — which
// RECONCILE install state to the config they are handed and prune records that
// config does not reference. Run against a developer's or CI runner's actual
// ~/.local/state/conductor, a test whose fixture config references (say) no `js`
// engine would delete the real js plugin from the live manifest — silently
// breaking the running daemon on its next restart. That is exactly the
// recurring "js not installed" wipe.
//
// testhome also clears XDG_CONFIG_HOME / XDG_STATE_HOME / CONDUCTOR_CONFIG (a CI
// runner sets XDG_CONFIG_HOME; GitHub's does), so HOME decides, as every test
// that points HOME at a temp dir expects; tests that set them still win.
func TestMain(m *testing.M) { testhome.Main(m) }
