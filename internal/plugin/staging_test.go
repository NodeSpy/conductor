package plugin

import (
	"os"
	"path/filepath"
	"strings"
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

// A plugin's environment is scrubbed except the variables it declared
// (Capabilities.Env, recorded in its install manifest) — passed through
// from the daemon's own environment — and never conductor's own.
func TestDeclaredEnvIsPassedThrough(t *testing.T) {
	t.Setenv("ACME_TOKEN", "tok")
	t.Setenv("OTHER_SECRET", "nope")
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "never")
	m := manifestFromDecl(&Decl{Capabilities: Capabilities{Env: []string{"ACME_TOKEN", "CONDUCTOR_SKILL_TOKEN", "MISSING"}}})
	bin := filepath.Join(t.TempDir(), "conductor-acme")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, cleanup, _, err := buildCommand(Spec{Name: "acme", BinPath: bin, Manifest: m}, SandboxDeps{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "ACME_TOKEN=tok") {
		t.Fatalf("declared variable not passed: %v", cmd.Env)
	}
	for _, leaked := range []string{"OTHER_SECRET", "CONDUCTOR_SKILL_TOKEN", "MISSING="} {
		if strings.Contains(env, leaked) {
			t.Fatalf("%s reached the plugin: %v", leaked, cmd.Env)
		}
	}
	if !strings.Contains(m.Summary(), "env ACME_TOKEN") {
		t.Fatalf("the permission summary must show it: %q", m.Summary())
	}
}
