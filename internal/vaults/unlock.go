package vaults

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// An unlock: reference is a vault's bootstrap — where the ONE secret that
// opens it lives. Because conductor auto-updates and restarts itself, the
// bootstrap must resolve without a human present. The forms, best for
// headless first:
//
//	creds:            $CREDENTIALS_DIRECTORY/conductor-vault-key (systemd-creds)
//	creds:name        $CREDENTIALS_DIRECTORY/<name>
//	keyring:          OS keyring, service "conductor-vault-key"
//	keyring:service   OS keyring, named service
//	env:VAR           process env / conductor.env
//	file:/path        a chmod-600 file (trailing newline trimmed)
//	(anything else)   the literal material itself — valid but discouraged;
//	                  prefer one of the indirections above
//
// An EMPTY unlock ref means "the default chain": the conductor type falls
// back to secrets.KeyChain (env → systemd-creds → keyring → sibling key
// file); other types treat it as "no bootstrap needed" (pass rides the GPG
// agent, onepassword can ride an op session).

// Bootstrap resolves one unlock reference. Lookups are injectable for tests.
type Bootstrap struct {
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
	Keyring   func(service string) string
}

// NewBootstrap returns the OS-backed bootstrap resolver.
func NewBootstrap() *Bootstrap {
	return &Bootstrap{
		LookupEnv: os.LookupEnv,
		ReadFile:  os.ReadFile,
		Keyring:   secrets.KeyringLookup,
	}
}

// Resolve returns the bootstrap material an unlock ref names. An empty ref
// returns ("", nil) — the caller applies its type's default.
func (b *Bootstrap) Resolve(ref string) (string, error) {
	switch {
	case ref == "":
		return "", nil
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		if name == "" {
			return "", fmt.Errorf("unlock %q: empty variable name", ref)
		}
		v, ok := b.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("unlock %q: environment variable %s is not set", ref, name)
		}
		return v, nil
	case strings.HasPrefix(ref, "file:"):
		path := strings.TrimPrefix(ref, "file:")
		if path == "" {
			return "", fmt.Errorf("unlock %q: empty file path", ref)
		}
		raw, err := b.ReadFile(expandHome(path))
		if err != nil {
			return "", fmt.Errorf("unlock %q: %w", ref, err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	case strings.HasPrefix(ref, "keyring:"):
		service := strings.TrimPrefix(ref, "keyring:")
		if service == "" {
			service = "conductor-vault-key"
		}
		v := b.Keyring(service)
		if v == "" {
			return "", fmt.Errorf("unlock %q: no keyring entry for service %q (or the keyring tool is unavailable)", ref, service)
		}
		return v, nil
	case strings.HasPrefix(ref, "creds:"):
		name := strings.TrimPrefix(ref, "creds:")
		if name == "" {
			name = "conductor-vault-key"
		}
		dir, ok := b.LookupEnv("CREDENTIALS_DIRECTORY")
		if !ok || dir == "" {
			return "", fmt.Errorf("unlock %q: $CREDENTIALS_DIRECTORY is not set (systemd credentials are unavailable)", ref)
		}
		raw, err := b.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("unlock %q: %w", ref, err)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}
	// Literal material. Valid — but an indirection keeps it out of config.
	return ref, nil
}

// expandHome expands a leading ~/ against the user's home directory.
func expandHome(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return h + p[1:]
		}
	}
	return p
}
