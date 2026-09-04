// The built-in vault is a single JSON file holding ONE secretbox blob: the
// whole {name -> value} map is serialized, padded to a fixed bucket, and
// sealed with a 32-byte master key — entry names and the entry count are
// ciphertext, and the blob length only reveals the size bucket. The file
// header records the scrypt parameters (profile chosen at `conductor vault
// init`, `--sensitive` for the hardened cost); the key itself is never stored
// in the vault file. The master key resolves via KeyChain, best for headless
// operation first, because the daemon restarts itself (auto-update) and must
// be able to unlock without a human:
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
// is treated as a passphrase and stretched with scrypt using the parameters
// recorded in the vault header.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
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

// vaultFile is the on-disk shape of the encrypted vault (version 2): the
// whole {name → value} map is serialized, padded to a fixed bucket, and
// sealed as ONE secretbox blob — entry names and the entry count are
// ciphertext, not JSON keys. The KDF parameters live in the header so unlock
// re-derives the same key; the master key itself is never in the file.
type vaultFile struct {
	Version int      `json:"version"`
	KDF     VaultKDF `json:"kdf"`
	Sealed  string   `json:"sealed"` // base64(nonce || box) over the padded entries JSON
}

// VaultKDF records the scrypt parameters a passphrase is stretched with.
type VaultKDF struct {
	N    int    `json:"n"`
	R    int    `json:"r"`
	P    int    `json:"p"`
	Salt string `json:"salt"` // base64, per vault
}

// vaultVersion is the format this build reads and writes.
const vaultVersion = 2

// vaultPadBucket is the padding granularity: the serialized entry map is
// padded up to a multiple of this before sealing, so the ciphertext length
// does not leak the entry count or sizes within a bucket.
const vaultPadBucket = 256

// KDF profiles selectable at `conductor vault init`.
const (
	// kdfInteractiveN is the default scrypt cost.
	kdfInteractiveN = 1 << 15
	// kdfSensitiveN is the `--sensitive` profile for vaults that may end up
	// somewhere public (e.g. committed): ~32× the work per guess.
	kdfSensitiveN = 1 << 20
)

// KDFProfile returns the scrypt parameters for a named profile:
// "" or "interactive" (N=2^15), "sensitive" (N=2^20). Salt is left empty —
// the vault generates its own.
func KDFProfile(name string) (VaultKDF, error) {
	switch name {
	case "", "interactive":
		return VaultKDF{N: kdfInteractiveN, R: 8, P: 1}, nil
	case "sensitive":
		return VaultKDF{N: kdfSensitiveN, R: 8, P: 1}, nil
	}
	return VaultKDF{}, fmt.Errorf("vault: unknown KDF profile %q (interactive|sensitive)", name)
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

// osKeyringKey reads the default vault key entry from the platform keyring.
func osKeyringKey() string { return KeyringLookup(keyringService) }

// KeyringLookup reads one entry from the platform keyring by shelling out to
// its CLI — macOS Keychain via `security`, libsecret via `secret-tool` —
// keeping the binary cgo-free. A missing tool, locked keyring, or absent
// entry returns "" so unlock chains can fall through. Exported for the
// vaults package's `keyring:` unlock references.
func KeyringLookup(service string) string {
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"security", "find-generic-password", "-s", service, "-w"}
	case "linux":
		argv = []string{"secret-tool", "lookup", "service", service}
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
// base64-decodes to exactly 32 bytes, else scrypt-stretched as a passphrase
// with the vault's recorded parameters.
func deriveKey(material []byte, kdf VaultKDF) (*[32]byte, error) {
	var key [32]byte
	if b, err := base64.StdEncoding.DecodeString(string(material)); err == nil && len(b) == 32 {
		copy(key[:], b)
		return &key, nil
	}
	salt, err := base64.StdEncoding.DecodeString(kdf.Salt)
	if err != nil || len(salt) == 0 {
		return nil, fmt.Errorf("vault: passphrase key material but the vault has no usable salt")
	}
	b, err := scrypt.Key(material, salt, kdf.N, kdf.R, kdf.P, 32)
	if err != nil {
		return nil, fmt.Errorf("vault: derive key: %w", err)
	}
	copy(key[:], b)
	return &key, nil
}

// Vault is an open (decrypted) vault: the entry map lives in memory and is
// serialized, padded, and sealed only on Save.
type Vault struct {
	Path    string
	key     *[32]byte
	kdf     VaultKDF
	entries map[string]string
}

// newSalt returns fresh base64 salt material.
func newSalt() (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(salt), nil
}

// InitVault creates a NEW vault at path with the named KDF profile
// ("interactive" default, "sensitive" for the hardened cost). It refuses to
// overwrite an existing vault.
func InitVault(path string, keyFn func() ([]byte, error), profile string) (*Vault, error) {
	if path == "" {
		path = DefaultVaultPath()
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("vault %s already exists — remove it (or `conductor vault add` to it) instead of re-initializing", path)
	}
	kdf, err := KDFProfile(profile)
	if err != nil {
		return nil, err
	}
	if kdf.Salt, err = newSalt(); err != nil {
		return nil, err
	}
	v := &Vault{Path: path, kdf: kdf, entries: map[string]string{}}
	if keyFn == nil {
		keyFn = func() ([]byte, error) { return KeyChain(path) }
	}
	material, err := keyFn()
	if err != nil {
		return nil, err
	}
	if v.key, err = deriveKey(material, v.kdf); err != nil {
		return nil, err
	}
	return v, nil
}

// OpenVault loads (or initializes in memory, for a missing file) the vault at
// path using the key material from keyFn (nil = KeyChain). Opening decrypts
// the whole entry map, so a wrong key fails here, not at first Get.
func OpenVault(path string, keyFn func() ([]byte, error)) (*Vault, error) {
	if path == "" {
		path = DefaultVaultPath()
	}
	v := &Vault{Path: path, entries: map[string]string{}}
	var f vaultFile
	exists := false
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("vault %s: %w", path, err)
		}
		if f.Version != vaultVersion {
			return nil, fmt.Errorf("vault %s: unsupported version %d — this build reads v%d (recreate it with `conductor vault init`)", path, f.Version, vaultVersion)
		}
		exists = true
		v.kdf = f.KDF
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("vault %s: %w", path, err)
	} else {
		kdf, _ := KDFProfile("")
		v.kdf = kdf
		salt, serr := newSalt()
		if serr != nil {
			return nil, serr
		}
		v.kdf.Salt = salt
	}
	if keyFn == nil {
		keyFn = func() ([]byte, error) { return KeyChain(path) }
	}
	material, err := keyFn()
	if err != nil {
		return nil, err
	}
	if v.key, err = deriveKey(material, v.kdf); err != nil {
		return nil, err
	}
	if exists {
		if v.entries, err = unseal(f.Sealed, v.key); err != nil {
			return nil, fmt.Errorf("vault %s: %w", path, err)
		}
	}
	return v, nil
}

// seal serializes the entry map, pads it to the bucket size (4-byte length
// prefix, zero fill), and seals the whole thing as one secretbox blob.
func seal(entries map[string]string, key *[32]byte) (string, error) {
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	if len(b) > int(^uint32(0)) {
		return "", fmt.Errorf("vault: entries too large")
	}
	total := 4 + len(b)
	padded := (total + vaultPadBucket - 1) / vaultPadBucket * vaultPadBucket
	plain := make([]byte, padded)
	binary.BigEndian.PutUint32(plain[:4], uint32(len(b)))
	copy(plain[4:], b)
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	out := secretbox.Seal(nonce[:], plain, &nonce, key)
	return base64.StdEncoding.EncodeToString(out), nil
}

// unseal reverses seal: open the blob, read the length prefix, unmarshal.
func unseal(sealed string, key *[32]byte) (map[string]string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < 24 {
		return nil, fmt.Errorf("vault: sealed blob is corrupt")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plain, ok := secretbox.Open(nil, raw[24:], &nonce, key)
	if !ok {
		return nil, fmt.Errorf("vault: wrong key or corrupt vault")
	}
	if len(plain) < 4 {
		return nil, fmt.Errorf("vault: sealed blob is corrupt")
	}
	n := binary.BigEndian.Uint32(plain[:4])
	if int(n) > len(plain)-4 {
		return nil, fmt.Errorf("vault: sealed blob is corrupt")
	}
	entries := map[string]string{}
	if err := json.Unmarshal(plain[4:4+n], &entries); err != nil {
		return nil, fmt.Errorf("vault: sealed blob is corrupt: %w", err)
	}
	return entries, nil
}

// Get returns one entry.
func (v *Vault) Get(name string) (string, error) {
	val, ok := v.entries[name]
	if !ok {
		return "", fmt.Errorf("vault: no entry %q (have: %s)", name, strings.Join(v.Names(), ", "))
	}
	return val, nil
}

// Set stores one entry (in memory; call Save to persist).
func (v *Vault) Set(name, value string) error {
	v.entries[name] = value
	return nil
}

// Delete removes one entry. Reports whether it existed.
func (v *Vault) Delete(name string) bool {
	_, ok := v.entries[name]
	delete(v.entries, name)
	return ok
}

// Names lists the entry names, sorted.
func (v *Vault) Names() []string {
	out := make([]string, 0, len(v.entries))
	for n := range v.entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KDF returns the vault's recorded key-derivation parameters.
func (v *Vault) KDF() VaultKDF { return v.kdf }

// Save seals the entry map and writes the vault atomically (0600).
func (v *Vault) Save() error {
	sealed, err := seal(v.entries, v.key)
	if err != nil {
		return err
	}
	f := vaultFile{Version: vaultVersion, KDF: v.kdf, Sealed: sealed}
	if err := os.MkdirAll(filepath.Dir(v.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
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
