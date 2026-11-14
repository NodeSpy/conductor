package config

import (
	"strings"
	"testing"
)

// `commands:` names a declared command's program file: a bare command name
// to an absolute path (after ~) of a file named for it. It reaches every
// plugin's ref, whichever kind uses it.
func TestCommandsBlock(t *testing.T) {
	c, err := loadYAML(t, "commands: { gh: /opt/homebrew/Cellar/gh/2.101.0/bin/gh, git: ~/bin/git }\n")
	if err != nil {
		t.Fatal(err)
	}
	want := c.CommandPaths()
	if want["gh"] != "/opt/homebrew/Cellar/gh/2.101.0/bin/gh" || !strings.HasSuffix(want["git"], "/bin/git") || strings.HasPrefix(want["git"], "~") {
		t.Fatalf("command paths = %v", want)
	}
	refs := c.PluginRefs()
	if len(refs) == 0 {
		t.Fatal("no plugin refs")
	}
	for k, r := range refs {
		if r.Commands["gh"] != want["gh"] {
			t.Errorf("ref %s: commands = %v, want %v", k, r.Commands, want)
		}
	}
	for _, bad := range []struct{ yaml, want string }{
		{"commands: { gh: bin/gh }\n", "not an absolute path"},
		{"commands: { gh: /opt/bin/gh-2 }\n", "is not named gh"},
		{"commands: { bin/gh: /opt/bin/gh }\n", "is not a command name"},
	} {
		if _, err := loadYAML(t, bad.yaml); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: err %v, want %q", strings.TrimSpace(bad.yaml), err, bad.want)
		}
	}
}
