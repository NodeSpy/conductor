// Package testhome keeps a test binary off the developer's real home.
//
// Conductor resolves almost everything it owns from $HOME: per-run dirs and
// clones under ~/.conductor, plugin and install state under ~/.local/state,
// config under ~/.config, Claude transcripts under ~/.claude, the vault key in
// the session keyring. A test that reaches one of those code paths without
// overriding it writes into — or prunes — the live daemon's own dirs on the
// machine it runs on (dispatch tests used to leave a merge_conflict-* run dir in
// ~/.conductor/runs on every run).
//
// Every test package calls Main from its TestMain, so the whole binary runs
// against a throwaway home; TestEveryTestPackageIsolatesHome fails a package
// that forgets. Tests that set their own HOME / XDG_* still win.
package testhome

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ambient is the environment a developer's shell (or a conductor-dispatched
// agent) may export that points a test at real state outside $HOME, or at a
// real secret. Tests that need one set it themselves.
var ambient = []string{
	"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
	"CONDUCTOR_CONFIG", "CONDUCTOR_VAULT_KEY", "CONDUCTOR_ENDPOINT",
	"CREDENTIALS_DIRECTORY", "PASEO_HOME", "CLAUDE_CONFIG_DIR", "AGENT_DECK_HOME",
}

// Main runs the package's tests against a throwaway home and exits.
func Main(m *testing.M) {
	root, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

// Isolate points HOME at a fresh temp dir and clears every override that
// would lead around it, returning the temp root for the caller to remove.
//
// The XDG_* dirs are UNSET rather than pointed into the temp dir, so they fall
// back to HOME: a test that moves HOME expects config and state to follow it.
// XDG_RUNTIME_DIR and the session bus are moved too, so a keyring lookup
// (`secret-tool`) finds no keyring instead of the developer's.
func Isolate() (string, error) {
	pinGoEnv()
	root, err := os.MkdirTemp("", "conductor-testhome-")
	if err != nil {
		return "", err
	}
	home, run := filepath.Join(root, "home"), filepath.Join(root, "run")
	for _, d := range []string{home, run} {
		if err := os.Mkdir(d, 0o700); err != nil {
			os.RemoveAll(root)
			return "", err
		}
	}
	for _, k := range ambient {
		os.Unsetenv(k)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_RUNTIME_DIR", run)
	os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(run, "bus"))
	return root, nil
}

// pinGoEnv fixes the Go toolchain's own dirs to what they resolve to under the
// REAL home before HOME moves. Tests that shell out to `go build` (the plugin
// fixtures) would otherwise start from an empty build cache in the temp home,
// and lose any `go env -w` settings. No `go` on PATH: nothing to pin.
func pinGoEnv() {
	keys := []string{"GOENV", "GOCACHE", "GOMODCACHE", "GOPATH"}
	out, err := exec.Command("go", append([]string{"env"}, keys...)...).Output()
	if err != nil {
		return
	}
	vals := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for i, k := range keys {
		if i < len(vals) && vals[i] != "" {
			os.Setenv(k, vals[i])
		}
	}
}
