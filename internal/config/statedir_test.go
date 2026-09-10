package config

import (
	"path/filepath"
	"testing"
)

// §14: StateDir hardwired $HOME/.local/state, ignoring XDG_STATE_HOME —
// the very variable that path exists to honour. A box that redirects its
// state (a container, a multi-tenant host, a test) wrote install state
// somewhere it did not expect and read another box's back.
func TestStateDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	SetStateDir("")
	if got, want := StateDir(), filepath.Join("/tmp/xdg-state", "conductor"); got != want {
		t.Fatalf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirFallsBackToTheSpecDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/fakehome")
	SetStateDir("")
	if got, want := StateDir(), "/tmp/fakehome/.local/state/conductor"; got != want {
		t.Fatalf("StateDir() = %q, want %q", got, want)
	}
}

// The explicit override wins over both — that is what --state-dir sets.
func TestStateDirOverrideWins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	SetStateDir("/tmp/explicit")
	t.Cleanup(func() { SetStateDir("") })
	if got := StateDir(); got != "/tmp/explicit" {
		t.Fatalf("StateDir() = %q, want the override", got)
	}
}
