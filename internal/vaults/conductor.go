package vaults

import (
	"context"
	"fmt"
	"sync"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// ConductorVault is the built-in hardened encrypted vault as a vault
// backend: one vault.json whose whole {name → value} map is sealed as a
// single padded secretbox blob (entry names and count are ciphertext), the
// master key never in the file, KDF profile recorded in the header (see
// internal/secrets/vault.go for the format and threat model). Fully
// read/write/list/delete capable.
type ConductorVault struct {
	path  string
	keyFn func() ([]byte, error)

	mu sync.Mutex
	v  *secrets.Vault // opened on first use
}

// NewConductorVault builds the backend for one conductor-type entry. path ""
// means the default vault location. unlockRef "" means the default
// secrets.KeyChain chain (env → systemd-creds → keyring → sibling key file);
// anything else resolves through boot.
func NewConductorVault(path, unlockRef string, boot *Bootstrap) *ConductorVault {
	if path == "" {
		path = secrets.DefaultVaultPath()
	} else {
		path = expandHome(path)
	}
	if boot == nil {
		boot = NewBootstrap()
	}
	keyFn := func() ([]byte, error) {
		if unlockRef == "" {
			return secrets.KeyChain(path)
		}
		m, err := boot.Resolve(unlockRef)
		if err != nil {
			return nil, err
		}
		return []byte(m), nil
	}
	return &ConductorVault{path: path, keyFn: keyFn}
}

// Path returns the vault file location (for the CLI and error messages).
func (c *ConductorVault) Path() string { return c.path }

// Unlock opens (and caches) the underlying vault — decrypting the whole
// entry map, so a wrong key fails here with the unlock error, not at first
// Get. Used at build for the boots-anyway check and by every op.
func (c *ConductorVault) Unlock() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.openLocked()
	return err
}

func (c *ConductorVault) openLocked() (*secrets.Vault, error) {
	if c.v != nil {
		return c.v, nil
	}
	v, err := secrets.OpenVault(c.path, c.keyFn)
	if err != nil {
		return nil, err
	}
	c.v = v
	return v, nil
}

// Read returns one entry.
func (c *ConductorVault) Read(ctx context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.openLocked()
	if err != nil {
		return "", err
	}
	return v.Get(key)
}

// Write stores one entry and persists the vault (atomic 0600 rewrite).
func (c *ConductorVault) Write(ctx context.Context, key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.openLocked()
	if err != nil {
		return err
	}
	if err := v.Set(key, value); err != nil {
		return err
	}
	return v.Save()
}

// Delete removes one entry and persists.
func (c *ConductorVault) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.openLocked()
	if err != nil {
		return err
	}
	if !v.Delete(key) {
		return fmt.Errorf("no entry %q", key)
	}
	return v.Save()
}

// List returns the entry names, sorted.
func (c *ConductorVault) List(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, err := c.openLocked()
	if err != nil {
		return nil, err
	}
	return v.Names(), nil
}
