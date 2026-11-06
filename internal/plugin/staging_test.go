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

// A plugin's environment is scrubbed except the variables it declares
// (Capabilities.Env), narrowed by allow_env when the operator set one —
// installing the plugin is the user's consent to what it declares, so an
// EMPTY allow_env leaves the declaration as-is rather than denying
// everything (pluginGrant, jail.go) — and conductor's own control variables
// never pass, declared or not (spawn.go's withDeclaredEnv).
func TestDeclaredEnvIsPassedThrough(t *testing.T) {
	t.Setenv("ACME_TOKEN", "tok")
	t.Setenv("OTHER_SECRET", "nope")
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "never")
	m := manifestFromDecl(&Decl{Capabilities: Capabilities{Env: []string{"ACME_TOKEN", "CONDUCTOR_SKILL_TOKEN", "MISSING"}}})
	bin := filepath.Join(t.TempDir(), "conductor-acme")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// This is a real confinement property of the jail, so it is proven
	// through a real jailed launch (testJailDeps), not a bare SandboxDeps{}
	// that would merely fail with "conductor's own binary is not wired".

	// No allow_env at all: a declared variable passes on the strength of the
	// declaration alone. conductor's own control variables never do.
	cmd0, cleanup0, _, err := buildCommand(Spec{Name: "acme", BinPath: bin, Manifest: m}, testJailDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	cleanup0()
	env0 := strings.Join(cmd0.Env, "\n")
	if !strings.Contains(env0, "ACME_TOKEN=tok") {
		t.Fatalf("a declared variable must pass with no allow_env narrowing: %v", cmd0.Env)
	}
	if strings.Contains(env0, "CONDUCTOR_SKILL_TOKEN") {
		t.Fatalf("conductor's own control variables must never pass, declared or not: %v", cmd0.Env)
	}

	// allow_env NARROWS a declared variable: naming a different subset drops
	// ACME_TOKEN even though it is declared.
	narrowed, cleanupN, _, err := buildCommand(Spec{Name: "acme", BinPath: bin, Manifest: m, AllowEnv: []string{"MISSING"}}, testJailDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	cleanupN()
	if strings.Contains(strings.Join(narrowed.Env, "\n"), "ACME_TOKEN") {
		t.Fatal("allow_env must narrow a declared variable out when it is not named")
	}

	// Granted but not declared (OTHER_SECRET): still nothing — allow_env can
	// only narrow the declaration, never widen it.
	cmd, cleanup, _, err := buildCommand(Spec{Name: "acme", BinPath: bin, Manifest: m,
		AllowEnv: []string{"ACME_TOKEN", "CONDUCTOR_SKILL_TOKEN", "OTHER_SECRET", "MISSING"}}, testJailDeps(t))
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
	if !strings.Contains(m.Summary(), "reads env: ACME_TOKEN") {
		t.Fatalf("the permission summary must show it: %q", m.Summary())
	}
}
