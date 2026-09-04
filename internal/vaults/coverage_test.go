package vaults

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestExecHelper runs the real exec path against ubiquitous binaries.
func TestExecHelper(t *testing.T) {
	ctx := context.Background()
	out, err := execHelper(ctx, "", nil, "sh", "-c", "echo real-exec-out")
	if err != nil || out != "real-exec-out" {
		t.Fatalf("stdout: %q %v", out, err)
	}
	// stdin reaches the command; env additions apply.
	out, err = execHelper(ctx, "from-stdin\n", []string{"XTEST=env-through"}, "sh", "-c", "cat; printf %s \"$XTEST\"")
	if err != nil || out != "from-stdin\nenv-through" {
		t.Fatalf("stdin/env: %q %v", out, err)
	}
	// A missing binary names itself; stderr surfaces on failure.
	if _, err := execHelper(ctx, "", nil, "definitely-not-a-binary-xyz"); err == nil ||
		!strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("missing binary: %v", err)
	}
	if _, err := execHelper(ctx, "", nil, "sh", "-c", "echo boom >&2; exit 3"); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr surfaced: %v", err)
	}
}

// TestPreloadListable: listable vaults preload their entries (tainted);
// path-keyed and broken vaults are skipped.
func TestPreloadListable(t *testing.T) {
	resetReg(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("preload-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Register("files", "file", &FileVault{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := Register("op", "onepassword", &OnePasswordVault{}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterBroken("hcv", "hashicorp", "no token"); err != nil {
		t.Fatal(err)
	}
	var tainted []string
	SetTaint(func(v string) { tainted = append(tainted, v) })

	got := PreloadListable(context.Background())
	if len(got) != 1 || got["files"]["gh"] != "preload-secret" {
		t.Fatalf("preload: %#v", got)
	}
	if len(tainted) != 1 || tainted[0] != "preload-secret" {
		t.Fatalf("preload taint: %v", tainted)
	}
}

// TestConductorPathAndExpandHome: the CLI-facing accessors.
func TestConductorPathAndExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := NewConductorVault("~/sub/vault.json", "k", nil)
	if c.Path() != filepath.Join(home, "sub/vault.json") {
		t.Fatalf("path: %q", c.Path())
	}
	if got := expandHome("~"); got != home {
		t.Fatalf("expandHome(~): %q", got)
	}
	if got := expandHome("/abs"); got != "/abs" {
		t.Fatalf("expandHome(/abs): %q", got)
	}
	// Default path when none configured.
	c2 := NewConductorVault("", "k", nil)
	if c2.Path() != secrets.DefaultVaultPath() {
		t.Fatalf("default path: %q", c2.Path())
	}
}

// TestEmptyNameErrors: registry guards.
func TestEmptyNameErrors(t *testing.T) {
	resetReg(t)
	if err := RegisterBroken("", "x", "r"); err == nil {
		t.Fatal("empty broken name must error")
	}
	if err := Write(context.Background(), "", "k", "v"); err == nil ||
		!strings.Contains(err.Error(), "none — add a vaults: section") {
		t.Fatalf("empty registry list: %v", err)
	}
	// A broken duplicate collides like a healthy one.
	if err := RegisterBroken("dup", "x", "r"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterBroken("dup", "x", "r"); err == nil {
		t.Fatal("duplicate broken register must error")
	}
}
