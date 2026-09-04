package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testResolver() *Resolver {
	r := New()
	r.LookupEnv = func(k string) (string, bool) {
		if k == "GH_PAT" {
			return "tok-abc123", true
		}
		return "", false
	}
	return r
}

func TestIsRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"env:GH_PAT":            true,
		"op://Vault/Item/field": true,
		"pass:conductor/gh":     true,
		"vault:gh-token":        true,
		"file:/run/secrets/gh":  true,
		"ghp_plaintoken":        false,
		"":                      false,
		"xoxb-123":              false,
		"https://example.com":   false,
	} {
		if got := IsRef(ref); got != want {
			t.Errorf("IsRef(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestResolveEnv(t *testing.T) {
	r := testResolver()
	v, err := r.Resolve(context.Background(), "env:GH_PAT")
	if err != nil || v != "tok-abc123" {
		t.Fatalf("env resolve: %q, %v", v, err)
	}
	if _, err := r.Resolve(context.Background(), "env:MISSING"); err == nil {
		t.Fatal("missing env var should error")
	} else if !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("error should name the variable: %v", err)
	}
}

func TestResolveLiteralPassthrough(t *testing.T) {
	r := testResolver()
	v, err := r.Resolve(context.Background(), "ghp_literal")
	if err != nil || v != "ghp_literal" {
		t.Fatalf("literal passthrough: %q, %v", v, err)
	}
	// Literals are not auto-tracked for redaction.
	if got := r.Redact("x ghp_literal y"); got != "x ghp_literal y" {
		t.Errorf("literal should not be redacted without Track: %q", got)
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	os.WriteFile(p, []byte("file-secret-value\n"), 0o600)
	r := testResolver()
	v, err := r.Resolve(context.Background(), "file:"+p)
	if err != nil || v != "file-secret-value" {
		t.Fatalf("file resolve: %q, %v", v, err)
	}
}

func TestResolveOpAndPass(t *testing.T) {
	r := testResolver()
	var calls []string
	r.Exec = func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name {
		case "op":
			return "op-secret", nil
		case "pass":
			return "pass-secret\nextra: metadata", nil
		}
		return "", fmt.Errorf("unexpected helper %s", name)
	}
	if v, err := r.Resolve(context.Background(), "op://Private/GitHub/token"); err != nil || v != "op-secret" {
		t.Fatalf("op resolve: %q, %v", v, err)
	}
	if v, err := r.Resolve(context.Background(), "pass:conductor/gh-token"); err != nil || v != "pass-secret" {
		t.Fatalf("pass resolve (first line only): %q, %v", v, err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "op read") || !strings.HasPrefix(calls[1], "pass show conductor/gh-token") {
		t.Fatalf("helper calls: %v", calls)
	}
	// Cached: a second resolve must not re-run the helper.
	if _, err := r.Resolve(context.Background(), "op://Private/GitHub/token"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected cache hit, helpers ran %d times", len(calls))
	}
}

func TestRedact(t *testing.T) {
	r := testResolver()
	if _, err := r.Resolve(context.Background(), "env:GH_PAT"); err != nil {
		t.Fatal(err)
	}
	got := r.Redact("posting with token tok-abc123 to api")
	if strings.Contains(got, "tok-abc123") || !strings.Contains(got, Placeholder) {
		t.Errorf("Redact left the secret in place: %q", got)
	}
	// Tracked literals are redacted too.
	r.Track("xoxb-999")
	if got := r.Redact("bot xoxb-999"); strings.Contains(got, "xoxb-999") {
		t.Errorf("tracked literal not redacted: %q", got)
	}
	// Values shorter than 4 bytes are never tracked (would shred text).
	r.Track("ab")
	if got := r.Redact("ab initio"); got != "ab initio" {
		t.Errorf("short value should not be redacted: %q", got)
	}
}

func TestRedactValue(t *testing.T) {
	r := testResolver()
	r.Track("s3cret99")
	in := map[string]any{
		"text":  "the s3cret99 value",
		"list":  []any{"s3cret99", 42},
		"strs":  []string{"a s3cret99"},
		"inner": map[string]any{"tok": "s3cret99"},
		"n":     7,
	}
	out := r.RedactValue(in).(map[string]any)
	b := fmt.Sprintf("%v", out)
	if strings.Contains(b, "s3cret99") {
		t.Errorf("RedactValue left secret: %s", b)
	}
	if out["n"] != 7 {
		t.Errorf("non-string values must pass through")
	}
}

func TestVaultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	material, e := GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	keyFn := func() ([]byte, error) { return []byte(material), nil }

	v, e := OpenVault(path, keyFn)
	if e != nil {
		t.Fatal(e)
	}
	if e := v.Set("gh-token", "vault-secret"); e != nil {
		t.Fatal(e)
	}
	if e := v.Save(); e != nil {
		t.Fatal(e)
	}
	// File must not contain the plaintext.
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "vault-secret") {
		t.Fatal("vault file contains plaintext")
	}

	// Reopen and read back through the resolver.
	r := testResolver()
	r.VaultPath = path
	r.VaultKey = keyFn
	got, e := r.Resolve(context.Background(), "vault:gh-token")
	if e != nil || got != "vault-secret" {
		t.Fatalf("vault resolve: %q, %v", got, e)
	}

	// Wrong key fails closed.
	bad, e := GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	v2, e := OpenVault(path, func() ([]byte, error) { return []byte(bad), nil })
	if e != nil {
		t.Fatal(e)
	}
	if _, e := v2.Get("gh-token"); e == nil {
		t.Fatal("wrong key must not decrypt")
	}

	// Unknown entry names the known ones.
	v3, _ := OpenVault(path, keyFn)
	if _, e := v3.Get("nope"); e == nil || !strings.Contains(e.Error(), "gh-token") {
		t.Fatalf("unknown entry error should list names: %v", e)
	}
}

func TestVaultPassphraseKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")
	keyFn := func() ([]byte, error) { return []byte("correct horse battery staple"), nil }
	v, e := OpenVault(path, keyFn)
	if e != nil {
		t.Fatal(e)
	}
	if e := v.Set("x", "y"); e != nil {
		t.Fatal(e)
	}
	if e := v.Save(); e != nil {
		t.Fatal(e)
	}
	v2, e := OpenVault(path, keyFn)
	if e != nil {
		t.Fatal(e)
	}
	if got, e := v2.Get("x"); e != nil || got != "y" {
		t.Fatalf("passphrase round-trip: %q, %v", got, e)
	}
}

func TestVaultDeleteAndNames(t *testing.T) {
	dir := t.TempDir()
	keyFn := func() ([]byte, error) { m, _ := GenerateKey(); return []byte(m), nil }
	material, _ := GenerateKey()
	keyFn = func() ([]byte, error) { return []byte(material), nil }
	v, e := OpenVault(filepath.Join(dir, "v.json"), keyFn)
	if e != nil {
		t.Fatal(e)
	}
	v.Set("b", "1")
	v.Set("a", "2")
	if names := v.Names(); len(names) != 2 || names[0] != "a" {
		t.Fatalf("names: %v", names)
	}
	if !v.Delete("a") || v.Delete("a") {
		t.Fatal("delete semantics")
	}
}

func TestKeyChainSeedFile(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.json")
	t.Setenv(EnvVaultKey, "")
	os.Unsetenv(EnvVaultKey)
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	os.Unsetenv("CREDENTIALS_DIRECTORY")

	if _, e := KeyChain(vaultPath); e == nil {
		t.Fatal("no key sources should error")
	}
	kf, e := SeedKeyFile(vaultPath, "material-123")
	if e != nil {
		t.Fatal(e)
	}
	if fi, _ := os.Stat(kf); fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}
	got, e := KeyChain(vaultPath)
	if e != nil || string(got) != "material-123" {
		t.Fatalf("keychain via file: %q, %v", got, e)
	}

	// Env var wins over the file.
	t.Setenv(EnvVaultKey, "env-material")
	got, e = KeyChain(vaultPath)
	if e != nil || string(got) != "env-material" {
		t.Fatalf("keychain via env: %q, %v", got, e)
	}
}

func TestCredentialsDirectoryKey(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "conductor-vault-key"), []byte("cred-material\n"), 0o600)
	t.Setenv(EnvVaultKey, "")
	os.Unsetenv(EnvVaultKey)
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	got, e := KeyChain(filepath.Join(t.TempDir(), "vault.json"))
	if e != nil || string(got) != "cred-material" {
		t.Fatalf("keychain via $CREDENTIALS_DIRECTORY: %q, %v", got, e)
	}
}

// TestKeyChainOSKeyring proves the unlock chain consults the OS keyring CLI
// (secret-tool on Linux) between the systemd credential and the key file,
// and that the env var still wins.
func TestKeyChainOSKeyring(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the linux secret-tool path")
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "secret-tool")
	script := "#!/bin/sh\n" +
		`[ "$1 $2 $3" = "lookup service conductor-vault-key" ] || exit 1` + "\n" +
		"printf 'keyring-material\\n'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv(EnvVaultKey, "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	vaultPath := filepath.Join(t.TempDir(), "vault.json") // no sibling vault.key

	got, err := KeyChain(vaultPath)
	if err != nil || string(got) != "keyring-material" {
		t.Fatalf("keyring lookup: %q, %v", got, err)
	}

	// The env var stays first in the chain.
	t.Setenv(EnvVaultKey, "env-material")
	if got, err := KeyChain(vaultPath); err != nil || string(got) != "env-material" {
		t.Fatalf("env should win over keyring: %q, %v", got, err)
	}

	// No tool on PATH and no key file → the chain errors, naming every source.
	t.Setenv(EnvVaultKey, "")
	t.Setenv("PATH", t.TempDir())
	if _, err := KeyChain(vaultPath); err == nil || !strings.Contains(err.Error(), "OS keyring") {
		t.Fatalf("want a no-key error naming the keyring, got %v", err)
	}

	// The key file remains the last resort.
	kf := filepath.Join(filepath.Dir(vaultPath), "vault.key")
	if err := os.WriteFile(kf, []byte("file-material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := KeyChain(vaultPath); err != nil || string(got) != "file-material" {
		t.Fatalf("key-file fallback: %q, %v", got, err)
	}
}
