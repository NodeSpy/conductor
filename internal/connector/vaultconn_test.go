package connector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// seedConductorVault creates a vault.json with one entry (gh=tok-abc123) and
// returns its path and base64 key material.
func seedConductorVault(t *testing.T) (path, key string) {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "vault.json")
	v, err := secrets.InitVault(path, func() ([]byte, error) { return []byte(key), nil }, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = v.Set("gh", "tok-abc123")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	return path, key
}

// buildVaultRegistry builds a registry from YAML with a stubbed bootstrap
// (VKEY resolves to the conductor key) and returns it plus the resolver.
func buildVaultRegistry(t *testing.T, y string, env map[string]string) (*Registry, *secrets.Resolver) {
	t.Helper()
	t.Cleanup(vaults.Reset)
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatal(err)
	}
	sec := secrets.New()
	boot := &vaults.Bootstrap{
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		ReadFile:  os.ReadFile,
		Keyring:   func(string) string { return "" },
	}
	reg, err := Build(&cfg, Deps{Secrets: sec, Config: &cfg, VaultBoot: boot})
	if err != nil {
		t.Fatal(err)
	}
	return reg, sec
}

// TestVaultInstancesAndVerbs: each vaults: entry surfaces as an instance
// with read (and write on writable backends); reads taint into the
// resolver's redaction; writes persist; a read-only vault publishes no write
// verb and rejects a direct write with the capability error.
func TestVaultInstancesAndVerbs(t *testing.T) {
	vpath, key := seedConductorVault(t)
	fdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fdir, "api-key"), []byte("file-secret-9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, sec := buildVaultRegistry(t, `
connectors: {}
vaults:
  house: { type: conductor, path: `+vpath+`, unlock: { key: "env:VKEY" } }
  files: { type: file, dir: `+fdir+` }
`, map[string]string{"VKEY": key})
	ctx := context.Background()

	house, ok := reg.Get("house")
	if !ok || house.DisabledReason != "" {
		t.Fatalf("house instance: %+v", house)
	}
	if got := house.Decl.VerbNames(); strings.Join(got, ",") != "read,write" {
		t.Fatalf("house verbs: %v", got)
	}
	files, _ := reg.Get("files")
	if got := files.Decl.VerbNames(); strings.Join(got, ",") != "read" {
		t.Fatalf("files verbs (read-only): %v", got)
	}

	out, err := house.Invoke(ctx, "read", map[string]any{"key": "gh"})
	if err != nil || out["value"] != "tok-abc123" {
		t.Fatalf("read: %v %v", out, err)
	}
	// The read value is tainted: the resolver now redacts it.
	if got := sec.Redact("token is tok-abc123"); got != "token is "+secrets.Placeholder {
		t.Fatalf("redaction after read: %q", got)
	}

	if _, err := house.Invoke(ctx, "write", map[string]any{"key": "new", "value": "written-1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err = house.Invoke(ctx, "read", map[string]any{"key": "new"})
	if err != nil || out["value"] != "written-1" {
		t.Fatalf("read back: %v %v", out, err)
	}

	out, err = files.Invoke(ctx, "read", map[string]any{"key": "api-key"})
	if err != nil || out["value"] != "file-secret-9" {
		t.Fatalf("file read: %v %v", out, err)
	}
	// InvokeFinal rejects write on the read-only vault (no verb declared).
	if _, err := files.InvokeFinal(ctx, "write", map[string]any{"key": "x", "value": "y"}); err == nil ||
		!strings.Contains(err.Error(), `no verb "write"`) {
		t.Fatalf("write on read-only: %v", err)
	}
	// The registry-level write has the capability error too.
	if err := vaults.Write(ctx, "files", "x", "y"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("vaults.Write on read-only: %v", err)
	}

	// Resolver: a {{ vault … }} config reference resolves through the hook.
	v, err := sec.Resolve(ctx, `{{ vault "house" "gh" }}`)
	if err != nil || v != "tok-abc123" {
		t.Fatalf("resolver vault ref: %q %v", v, err)
	}
	v, err = sec.Resolve(ctx, `{{ .vaults.house.gh }}`)
	if err != nil || v != "tok-abc123" {
		t.Fatalf("resolver .vaults ref: %q %v", v, err)
	}
	if _, err := sec.Resolve(ctx, `{{ vault "ghost" "k" }}`); err == nil || !strings.Contains(err.Error(), `no vault named "ghost"`) {
		t.Fatalf("unknown vault ref: %v", err)
	}
}

// TestVaultUnlockFailureDisablesNotCrashes: a vault whose unlock can't
// resolve registers broken — Build succeeds, the instance is disabled with
// the reason, and every use names it.
func TestVaultUnlockFailureDisablesNotCrashes(t *testing.T) {
	vpath, _ := seedConductorVault(t)
	reg, _ := buildVaultRegistry(t, `
connectors: {}
vaults:
  house: { type: conductor, path: `+vpath+`, unlock: { key: "env:MISSING_KEY" } }
`, nil)
	in, ok := reg.Get("house")
	if !ok {
		t.Fatal("instance must exist")
	}
	if !strings.Contains(in.DisabledReason, "MISSING_KEY") {
		t.Fatalf("disabled reason: %q", in.DisabledReason)
	}
	if vaults.Broken("house") == "" {
		t.Fatal("registry must record the break")
	}
	if _, err := in.Invoke(context.Background(), "read", map[string]any{"key": "gh"}); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("read on broken vault: %v", err)
	}
}

// TestVaultStructuralErrors: config bugs are LOAD errors from Build.
func TestVaultStructuralErrors(t *testing.T) {
	for _, c := range []struct{ name, yaml, want string }{
		{"unknown type", "connectors: {}\nvaults:\n  x: { type: dynamo }\n", `unknown type "dynamo"`},
		{"file no dir", "connectors: {}\nvaults:\n  x: { type: file }\n", "dir: is required"},
		{"hashicorp no addr", "connectors: {}\nvaults:\n  x: { type: hashicorp }\n", "addr: is required"},
	} {
		t.Cleanup(vaults.Reset)
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(c.yaml), &cfg); err != nil {
			t.Fatal(err)
		}
		_, err := Build(&cfg, Deps{Secrets: secrets.New(), Config: &cfg})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// TestVaultAvailabilityIsNotALoadError: a missing file-vault directory and a
// missing hashicorp token disable the vault; Build still succeeds.
func TestVaultAvailabilityIsNotALoadError(t *testing.T) {
	reg, _ := buildVaultRegistry(t, `
connectors: {}
vaults:
  files: { type: file, dir: /nope/never/exists }
  hcv:   { type: hashicorp, addr: "https://vault.internal", unlock: { token: "env:MISSING" } }
`, nil)
	for _, name := range []string{"files", "hcv"} {
		in, _ := reg.Get(name)
		if in.DisabledReason == "" {
			t.Errorf("%s: expected a disabled reason", name)
		}
	}
}

// TestVaultNameValidation: name collisions with connectors and the reserved
// verb namespaces are config errors.
func TestVaultNameValidation(t *testing.T) {
	for _, c := range []struct{ name, yaml, want string }{
		{"collides with connector", `
connectors:
  gh: { type: command }
vaults:
  gh: { type: pass }
`, "collides with a connector"},
		{"reserved kv", "connectors: { c: { type: command } }\nvaults:\n  kv: { type: pass }\n", "reserved"},
		{"reserved sql", "connectors: { c: { type: command } }\nvaults:\n  sql: { type: pass }\n", "reserved"},
		{"missing type", "connectors: { c: { type: command } }\nvaults:\n  x: {}\n", "missing type"},
	} {
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(c.yaml), &cfg); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// TestConnectorCredentialFromVault: a connector connection field holding a
// {{ vault … }} reference resolves at build — the vault builds first.
func TestConnectorCredentialFromVault(t *testing.T) {
	vpath, key := seedConductorVault(t)
	reg, _ := buildVaultRegistry(t, `
connectors:
  api:
    type: rest
    base_url: http://x.example
    auth: { type: bearer, token: '{{ vault "house" "gh" }}' }
    verbs: { v: { method: GET, path: / } }
vaults:
  house: { type: conductor, path: `+vpath+`, unlock: { key: "env:VKEY" } }
`, map[string]string{"VKEY": key})
	in, _ := reg.Get("api")
	if in.DisabledReason != "" {
		t.Fatalf("api disabled: %s", in.DisabledReason)
	}
}

// seedConductorVaultEntries creates a vault.json with the given entries and
// returns its path and base64 key material.
func seedConductorVaultEntries(t *testing.T, entries map[string]string) (path, key string) {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "vault.json")
	v, err := secrets.InitVault(path, func() ([]byte, error) { return []byte(key), nil }, "")
	if err != nil {
		t.Fatal(err)
	}
	for k, val := range entries {
		_ = v.Set(k, val)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	return path, key
}
