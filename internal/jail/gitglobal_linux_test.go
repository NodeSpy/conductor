//go:build linux

package jail

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// An operator whose global git config is where GIT_CONFIG_GLOBAL says (not
// ~/.gitconfig): the jail passes the variable through, so it shows the file
// too, read-only — else the agent's commits carry no identity and are never
// signed. Never a file inside conductor's own state/config.
func TestJailShowsGitConfigGlobalReadOnly(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "gitconfig")
	os.WriteFile(cfg, []byte("[commit]\n\tgpgsign = true\n"), 0o600)
	state := filepath.Join(root, "state")
	os.MkdirAll(state, 0o700)
	m := &Manager{Root: filepath.Join(state, "jails"), Home: filepath.Join(root, "home"), Sensitive: []string{state},
		SelfExe: func() (string, error) { return "/bin/true", nil }, LookPath: func(string) (string, error) { return "", os.ErrNotExist }}
	d := &Dispatch{Dir: t.TempDir()}
	has := func(binds []sandbox.BindMount, p string) bool {
		for _, b := range binds {
			if b.Path == p && b.RO && b.Src == "" {
				return true
			}
		}
		return false
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	binds, err := m.layout(d, "/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if !has(binds, cfg) {
		t.Fatalf("GIT_CONFIG_GLOBAL's file must be in the jail, read-only: %+v", binds)
	}
	inState := filepath.Join(state, "gitconfig")
	os.WriteFile(inState, []byte("x"), 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", inState)
	if binds, _ := m.layout(d, "/bin/true"); has(binds, inState) {
		t.Fatal("a GIT_CONFIG_GLOBAL inside conductor's state must not be shown")
	}
}
