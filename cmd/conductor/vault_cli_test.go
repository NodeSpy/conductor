package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// withStdin runs fn with os.Stdin fed from the given string.
func withStdin(t *testing.T, input string, fn func() error) error {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = old }()
	return fn()
}

// writeVaultCLIConfig writes a config declaring one conductor vault (at a
// temp path, unlocked by a literal passphrase) and one read-only file vault.
func writeVaultCLIConfig(t *testing.T) (cfgPath, vaultPath string) {
	t.Helper()
	dir := t.TempDir()
	vaultPath = filepath.Join(dir, "vault.json")
	fdir := filepath.Join(dir, "filesec")
	if err := os.MkdirAll(fdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fdir, "api-key"), []byte("cli-file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(dir, "config.yaml")
	doc := `
connectors:
  box: { type: command }
vaults:
  house: { type: conductor, path: ` + vaultPath + `, unlock: { key: "cli-test-passphrase" } }
  files: { type: file, dir: ` + fdir + ` }
`
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, vaultPath
}

// TestVaultCLILifecycle: init → add → get → ls → rm against a named
// conductor vault, plus the capability and targeting errors.
func TestVaultCLILifecycle(t *testing.T) {
	cfgPath, vaultPath := writeVaultCLIConfig(t)
	run := func(input string, args ...string) (string, error) {
		var out string
		var err error
		ferr := withStdin(t, input, func() error {
			out, err = captureStdout(t, func() error {
				return cmdVault(append([]string{"--config", cfgPath}, args...))
			})
			return nil
		})
		if ferr != nil {
			t.Fatal(ferr)
		}
		return out, err
	}

	// init creates the vault at the entry's path with the entry's unlock.
	out, err := run("", "house", "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "initialized") {
		t.Fatalf("init output: %q", out)
	}
	if _, err := os.Stat(vaultPath); err != nil {
		t.Fatalf("vault file: %v", err)
	}
	// Re-init refuses.
	if _, err := run("", "house", "init"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("re-init: %v", err)
	}

	if _, err := run("gh-token-value\n", "house", "add", "gh"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err = run("", "house", "get", "gh")
	if err != nil || strings.TrimSpace(out) != "gh-token-value" {
		t.Fatalf("get: %q %v", out, err)
	}
	out, err = run("", "house", "ls")
	if err != nil || strings.TrimSpace(out) != "gh" {
		t.Fatalf("ls: %q %v", out, err)
	}
	if _, err := run("", "house", "rm", "gh"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	out, err = run("", "house", "ls")
	if err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("ls after rm: %q %v", out, err)
	}

	// The persisted vault opens with the same passphrase (KDF params from
	// the header).
	v, err := secrets.OpenVault(vaultPath, func() ([]byte, error) { return []byte("cli-test-passphrase"), nil })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(v.Names()); n != 0 {
		t.Fatalf("entries after rm: %d", n)
	}

	// Read-only backend: get/ls work, add/rm error clearly, init is
	// conductor-only.
	out, err = run("", "files", "get", "api-key")
	if err != nil || strings.TrimSpace(out) != "cli-file-secret" {
		t.Fatalf("file get: %q %v", out, err)
	}
	out, err = run("", "files", "ls")
	if err != nil || strings.TrimSpace(out) != "api-key" {
		t.Fatalf("file ls: %q %v", out, err)
	}
	if _, err := run("x\n", "files", "add", "k"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("file add: %v", err)
	}
	if _, err := run("", "files", "rm", "k"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("file rm: %v", err)
	}
	if _, err := run("", "files", "init"); err == nil || !strings.Contains(err.Error(), "conductor-type") {
		t.Fatalf("file init: %v", err)
	}

	// Targeting errors.
	if _, err := run("", "ghost", "ls"); err == nil || !strings.Contains(err.Error(), `no vault "ghost"`) {
		t.Fatalf("unknown vault: %v", err)
	}
	if _, err := run("", "house"); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("missing subcommand: %v", err)
	}
}
