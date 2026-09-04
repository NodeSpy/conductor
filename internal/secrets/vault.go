// The built-in vault is a single JSON file of secretbox-encrypted entries.
// Each entry is sealed with a 32-byte master key; the key itself is never
// stored in the vault file. The master key resolves via KeyChain, best for
// headless operation first, because the daemon restarts itself (auto-update)
// and must be able to unlock without a human:
//
//  1. $CONDUCTOR_VAULT_KEY — base64 key material, or a passphrase (systemd
//     `Environment=`, or conductor.env).
//  2. $CREDENTIALS_DIRECTORY/conductor-vault-key — a systemd encrypted
//     credential (`systemd-creds encrypt`, TPM/host-bound; see the wiki).
//  3. The OS keyring, via the platform CLI (no cgo): macOS Keychain
//     (`security find-generic-password -s conductor-vault-key -w`) or
//     libsecret (`secret-tool lookup service conductor-vault-key`).
//     Automatic once the login session is unlocked; absent tool or entry
//     falls through silently.
//  4. <vault dir>/vault.key — a chmod-600 key file seeded by
//     `conductor vault init` or `conductor unlock`.
//
// Key material that decodes as 32 base64 bytes is used directly; anything else
// is treated as a passphrase and stretched with scrypt against the vault's
// per-file salt.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

// vaultFile is the on-disk shape of the encrypted vault.
type vaultFile struct {
	Version int               `json:"version"`
	Salt    string            `json:"salt"`    // base64, for passphrase-derived keys
	Entries map[string]string `json:"entries"` // name -> base64(nonce || box)
}

// DefaultVaultPath returns the default vault location: ~/.config/conductor/vault.json.
func DefaultVaultPath() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "vault.json"
	}
	return filepath.Join(h, ".config/conductor/vault.json")
}

// EnvVaultKey is the environment variable KeyChain checks first.
const EnvVaultKey = "CONDUCTOR_VAULT_KEY"

// keyFileName is the sibling key file KeyChain checks last.
const keyFileName = "vault.key"

// keyringService is the service/name the OS-keyring lookup uses on both
// platforms (`security -s`, `secret-tool service`).
const keyringService = "conductor-vault-key"

// KeyChain returns the raw vault key material from the documented lookup
// order. It returns the material as found — LoadVault/deriveKey decide whether
// it is a direct key or a passphrase.
func KeyChain(vaultPath string) ([]byte, error) {
	if v := os.Getenv(EnvVaultKey); v != "" {
		return []byte(strings.TrimSpace(v)), nil
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, "conductor-vault-key")); err == nil {
			return []byte(strings.TrimSpace(string(b))), nil
		}
	}
	if v := osKeyringKey(); v != "" {
		return []byte(v), nil
	}
	kf := filepath.Join(filepath.Dir(vaultPath), keyFileName)
	if b, err := os.ReadFile(kf); err == nil {
		return []byte(strings.TrimSpace(string(b))), nil
	}
	return nil, fmt.Errorf("no vault key: set %s, provide a systemd credential conductor-vault-key, store one in the OS keyring as %q, or seed %s (`conductor vault init`)", EnvVaultKey, keyringService, kf)
}

// osKeyringKey reads the vault key from the platform keyring by shelling out
// to its CLI — macOS Keychain via `security`, libsecret via `secret-tool` —
// keeping the binary cgo-free. A missing tool, locked keyring, or absent
// entry returns "" so the chain falls through to the key file.
func osKeyringKey() string {
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"security", "find-generic-password", "-s", keyringService, "-w"}
	case "linux":
		argv = []string{"secret-tool", "lookup", "service", keyringService}
	default:
		return ""
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return ""
	}
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// deriveKey turns key material into the 32-byte secretbox key: direct if it
// base64-decodes to exactly 32 bytes, else scrypt-stretched as a passphrase.
func deriveKey(material, salt []byte) (*[32]byte, error) {
	var key [32]byte
	if b, err := base64.StdEncoding.DecodeString(string(material)); err == nil && len(b) == 32 {
		copy(key[:], b)
		return &key, nil
	}
	if len(salt) == 0 {
		return nil, fmt.Errorf("vault: passphrase key material but the vault has no salt")
	}
	b, err := scrypt.Key(material, salt, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("vault: derive key: %w", err)
	}
	copy(key[:], b)
	return &key, nil
}

// Vault is an open (decryptable) vault.
type Vault struct {
	Path string
	key  *[32]byte
	file vaultFile
}

// OpenVault loads (or initializes in memory) the vault at path using the key
// material from keyFn (nil = KeyChain).
func OpenVault(path string, keyFn func() ([]byte, error)) (*Vault, error) {
	if path == "" {
		path = DefaultVaultPath()
	}
	v := &Vault{Path: path, file: vaultFile{Version: 1, Entries: map[string]string{}}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &v.file); err != nil {
			return nil, fmt.Errorf("vault %s: %w", path, err)
		}
		if v.file.Entries == nil {
			v.file.Entries = map[string]string{}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("vault %s: %w", path, err)
	}
	if v.file.Salt == "" {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		v.file.Salt = base64.StdEncoding.EncodeToString(salt)
	}
	if keyFn == nil {
		keyFn = func() ([]byte, error) { return KeyChain(path) }
	}
	material, err := keyFn()
	if err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(v.file.Salt)
	if err != nil {
		return nil, fmt.Errorf("vault %s: bad salt: %w", path, err)
	}
	v.key, err = deriveKey(material, salt)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Get decrypts one entry.
func (v *Vault) Get(name string) (string, error) {
	enc, ok := v.file.Entries[name]
	if !ok {
		return "", fmt.Errorf("vault: no entry %q (have: %s)", name, strings.Join(v.Names(), ", "))
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw) < 24 {
		return "", fmt.Errorf("vault: entry %q is corrupt", name)
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plain, ok := secretbox.Open(nil, raw[24:], &nonce, v.key)
	if !ok {
		return "", fmt.Errorf("vault: entry %q: wrong key or corrupt entry", name)
	}
	return string(plain), nil
}

// Set encrypts and stores one entry (in memory; call Save to persist).
func (v *Vault) Set(name, value string) error {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	sealed := secretbox.Seal(nonce[:], []byte(value), &nonce, v.key)
	v.file.Entries[name] = base64.StdEncoding.EncodeToString(sealed)
	return nil
}

// Delete removes one entry. Reports whether it existed.
func (v *Vault) Delete(name string) bool {
	_, ok := v.file.Entries[name]
	delete(v.file.Entries, name)
	return ok
}

// Names lists the entry names, sorted.
func (v *Vault) Names() []string {
	out := make([]string, 0, len(v.file.Entries))
	for n := range v.file.Entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Save writes the vault atomically (0600).
func (v *Vault) Save() error {
	if err := os.MkdirAll(filepath.Dir(v.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v.file, "", "  ")
	if err != nil {
		return err
	}
	tmp := v.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.Path)
}

// GenerateKey returns fresh base64 key material for `conductor vault init`.
func GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// SeedKeyFile writes key material to the vault's sibling key file (0600),
// used by `conductor vault init` / `conductor unlock` to make the key
// retrievable across self-restarts without a human present.
func SeedKeyFile(vaultPath, material string) (string, error) {
	if vaultPath == "" {
		vaultPath = DefaultVaultPath()
	}
	kf := filepath.Join(filepath.Dir(vaultPath), keyFileName)
	if err := os.MkdirAll(filepath.Dir(kf), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(kf, []byte(material+"\n"), 0o600); err != nil {
		return "", err
	}
	return kf, nil
}

// vaultGet resolves a vault: reference through the Resolver's configured
// vault path and key function.
func (r *Resolver) vaultGet(name string) (string, error) {
	v, err := OpenVault(r.VaultPath, r.VaultKey)
	if err != nil {
		return "", err
	}
	return v.Get(name)
}
