package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// Each instance gets its own private staging directory under the plugin's
// subtree (plugin-contract.md Q7); an instance name cannot climb out of it,
// and no root means no staging at all.
func TestStagingDirPerInstance(t *testing.T) {
	root := t.TempDir()
	c := NewClient(Spec{Name: "chat"}, Deps{Sandbox: SandboxDeps{StagingRoot: root}})
	dir, err := c.StagingDir("work")
	if err != nil || dir != filepath.Join(root, "chat", "work") {
		t.Fatalf("StagingDir = %q, %v", dir, err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("staging dir not a private directory: %v %v", fi, err)
	}
	for _, bad := range []string{"", "../x", "a/b", `a\b`, ".hidden"} {
		if d, _ := c.StagingDir(bad); d != "" {
			t.Errorf("instance %q got staging dir %q", bad, d)
		}
	}
	if d, _ := NewClient(Spec{Name: "chat"}, Deps{}).StagingDir("work"); d != "" {
		t.Fatalf("no staging root must give no staging dir, got %q", d)
	}
}
