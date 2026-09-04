package secrets

import (
	"context"
	"encoding/json"
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

// TestLegacySchemesErrorLoudly: the replaced scheme URIs are still
// RECOGNIZED (IsRef true, so they never pass through as literal creds) but
// resolve to the migration-pointing error — no silent loss.
func TestLegacySchemesErrorLoudly(t *testing.T) {
	r := testResolver()
	for _, ref := range []string{
		"op://Private/GitHub/token", "pass:conductor/gh", "vault:gh-token", "file:/run/secrets/gh",
	} {
		if !IsRef(ref) {
			t.Errorf("IsRef(%q) must stay true", ref)
		}
		if _, err := r.Resolve(context.Background(), ref); err == nil ||
			!strings.Contains(err.Error(), "replaced by vaults:") {
			t.Errorf("Resolve(%q) = %v, want the migration pointer", ref, err)
		}
	}
}

// TestResolveVaultRef: a {{ vault … }} reference resolves through the
// VaultRead hook, caches, and tracks for redaction.
func TestResolveVaultRef(t *testing.T) {
	r := testResolver()
	calls := 0
	r.VaultRead = func(ctx context.Context, name, key string) (string, error) {
		calls++
		if name != "op" || key != "Private/GitHub/token" {
			t.Fatalf("hook args: %s %s", name, key)
		}
		return "hook-secret", nil
	}
	for i := 0; i < 2; i++ {
		v, err := r.Resolve(context.Background(), `{{ vault "op" "Private/GitHub/token" }}`)
		if err != nil || v != "hook-secret" {
			t.Fatalf("vault ref resolve: %q, %v", v, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected cache hit, hook ran %d times", calls)
	}
	if got := r.Redact("x hook-secret y"); !strings.Contains(got, Placeholder) {
		t.Errorf("resolved vault value should redact: %q", got)
	}
	// Without the hook, a vault ref errors clearly.
	r2 := testResolver()
	if _, err := r2.Resolve(context.Background(), `{{ vault "a" "b" }}`); err == nil ||
		!strings.Contains(err.Error(), "no vaults are configured") {
		t.Errorf("hookless vault ref: %v", err)
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

	// Reopen and read back.
	v2, e := OpenVault(path, keyFn)
	if e != nil {
		t.Fatal(e)
	}
	got, e := v2.Get("gh-token")
	if e != nil || got != "vault-secret" {
		t.Fatalf("vault read back: %q, %v", got, e)
	}

	// Wrong key fails closed — at open (the whole map unseals on load).
	bad, e := GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	if _, e := OpenVault(path, func() ([]byte, error) { return []byte(bad), nil }); e == nil {
		t.Fatal("wrong key must not open the vault")
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

// TestVaultHidesNamesAndCount: the v2 file leaks neither entry names (they
// are inside the sealed blob, not JSON keys) nor the entry count within a
// padding bucket (same sealed length for different small counts).
func TestVaultHidesNamesAndCount(t *testing.T) {
	material, _ := GenerateKey()
	keyFn := func() ([]byte, error) { return []byte(material), nil }

	sealedLen := func(t *testing.T, entries map[string]string) (int, []byte) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "vault.json")
		v, err := InitVault(path, keyFn, "")
		if err != nil {
			t.Fatal(err)
		}
		for k, val := range entries {
			v.Set(k, val)
		}
		if err := v.Save(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Sealed string `json:"sealed"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		return len(f.Sealed), raw
	}

	names := map[string]string{
		"gh-token": "secret-one", "slack-bot-token": "secret-two", "pagerduty-key": "secret-three",
	}
	n3, raw := sealedLen(t, names)
	for name := range names {
		if strings.Contains(string(raw), name) {
			t.Fatalf("entry name %q is plaintext in the vault file", name)
		}
	}
	for _, val := range names {
		if strings.Contains(string(raw), val) {
			t.Fatal("entry value is plaintext in the vault file")
		}
	}

	// One small entry vs three small entries: same bucket → same length.
	n1, _ := sealedLen(t, map[string]string{"gh-token": "secret-one"})
	if n1 != n3 {
		t.Fatalf("sealed length leaks entry count: 1 entry = %d bytes, 3 entries = %d bytes", n1, n3)
	}
}

// TestVaultKDFProfiles: init records the chosen scrypt cost in the header and
// a passphrase unlock reads it back.
func TestVaultKDFProfiles(t *testing.T) {
	pass := func() ([]byte, error) { return []byte("correct horse battery staple"), nil }

	path := filepath.Join(t.TempDir(), "vault.json")
	v, err := InitVault(path, pass, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.KDF().N != 1<<15 {
		t.Fatalf("default profile N = %d, want %d", v.KDF().N, 1<<15)
	}
	v.Set("x", "y")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	var f struct {
		KDF VaultKDF `json:"kdf"`
	}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.KDF.N != 1<<15 || f.KDF.R != 8 || f.KDF.P != 1 || f.KDF.Salt == "" {
		t.Fatalf("header KDF: %+v", f.KDF)
	}
	// Unlock re-derives with the recorded parameters.
	v2, err := OpenVault(path, pass)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v2.Get("x"); err != nil || got != "y" {
		t.Fatalf("passphrase reopen: %q, %v", got, err)
	}

	// The sensitive profile records the hardened cost. (Sealing only — no
	// passphrase stretch at 2^20 in a unit test; a direct key skips the KDF.)
	material, _ := GenerateKey()
	keyFn := func() ([]byte, error) { return []byte(material), nil }
	spath := filepath.Join(t.TempDir(), "vault.json")
	sv, err := InitVault(spath, keyFn, "sensitive")
	if err != nil {
		t.Fatal(err)
	}
	if sv.KDF().N != 1<<20 {
		t.Fatalf("sensitive profile N = %d, want %d", sv.KDF().N, 1<<20)
	}
	if err := sv.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(spath)
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.KDF.N != 1<<20 {
		t.Fatalf("sensitive header N = %d, want %d", f.KDF.N, 1<<20)
	}
	if _, err := OpenVault(spath, keyFn); err != nil {
		t.Fatalf("sensitive vault reopen: %v", err)
	}

	// Unknown profile is rejected.
	if _, err := KDFProfile("nope"); err == nil {
		t.Fatal("unknown profile should error")
	}
	// Init refuses to overwrite an existing vault.
	if _, err := InitVault(spath, keyFn, ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("re-init should refuse: %v", err)
	}
}

// TestVaultUnknownVersion: an unrecognized on-disk version fails cleanly.
func TestVaultUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.json")
	if err := os.WriteFile(path, []byte(`{"version": 1, "salt": "eA==", "entries": {"gh": "x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	material, _ := GenerateKey()
	_, err := OpenVault(path, func() ([]byte, error) { return []byte(material), nil })
	if err == nil || !strings.Contains(err.Error(), "unsupported version 1") {
		t.Fatalf("want an unsupported-version error, got %v", err)
	}
}

func TestExpandHomeAndExecHelper(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/x/y"); got != filepath.Join(home, "x/y") {
		t.Fatalf("expandHome: %q", got)
	}
	if got := expandHome("/abs"); got != "/abs" {
		t.Fatalf("absolute untouched: %q", got)
	}
	if got := expandHome("~other/x"); got != "~other/x" {
		t.Fatalf("~user untouched: %q", got)
	}
	// The real exec helper runs a process and errors on a missing one.
	if out, err := execHelper(context.Background(), "sh", "-c", "echo hi"); err != nil || !strings.Contains(out, "hi") {
		t.Fatalf("execHelper: %q %v", out, err)
	}
	if _, err := execHelper(context.Background(), "/nonexistent/bin"); err == nil {
		t.Fatal("missing helper must error")
	}
}

func TestInitVaultErrors(t *testing.T) {
	// Unknown profile.
	if _, err := InitVault(filepath.Join(t.TempDir(), "v.json"), func() ([]byte, error) { return []byte("k"), nil }, "nope"); err == nil {
		t.Fatal("unknown profile must error")
	}
	// Key function failure.
	if _, err := InitVault(filepath.Join(t.TempDir(), "v.json"), func() ([]byte, error) { return nil, os.ErrPermission }, ""); err == nil {
		t.Fatal("key failure must error")
	}
}

func TestSeedKeyFileDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kf, err := SeedKeyFile("", "material")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(kf, home) || !strings.HasSuffix(kf, "vault.key") {
		t.Fatalf("default seed path: %q", kf)
	}
}
