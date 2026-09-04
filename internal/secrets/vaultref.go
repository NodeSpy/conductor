package secrets

import (
	"context"
	"fmt"
	"strings"
)

// A vault reference is the one syntax for reading a declared vaults: entry
// from a config field: a string that IS exactly
//
//	{{ vault "<name>" "<key>" }}   or   {{ .vaults.<name>.<key> }}
//
// Inside larger templates (prompts, options with surrounding text) the flow
// engine's `vault` template function handles the same call; this parser
// covers whole-field references so connection credentials resolve at load.
// The parsing lives here (not in internal/vaults) so the resolver can
// recognize refs without an import cycle; internal/vaults re-exports it.

// ParseVaultRef reports whether s is exactly one vault reference and returns
// its vault name and key.
func ParseVaultRef(s string) (name, key string, ok bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{{") || !strings.HasSuffix(t, "}}") || strings.Count(t, "{{") != 1 {
		return "", "", false
	}
	inner := strings.TrimSpace(t[2 : len(t)-2])
	if rest, found := strings.CutPrefix(inner, ".vaults."); found {
		nm, k, cut := strings.Cut(rest, ".")
		if !cut || nm == "" || k == "" || strings.ContainsAny(rest, " \t|(){}\"'") {
			return "", "", false
		}
		return nm, k, true
	}
	rest, found := strings.CutPrefix(inner, "vault ")
	if !found {
		return "", "", false
	}
	parts, perr := splitQuoted(strings.TrimSpace(rest))
	if perr != nil || len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// IsVaultRef reports whether s is exactly one vault reference.
func IsVaultRef(s string) bool {
	_, _, ok := ParseVaultRef(s)
	return ok
}

// splitQuoted splits `"a" "b"` into its quoted parts (no escapes — vault
// names and keys never contain quotes).
func splitQuoted(s string) ([]string, error) {
	var out []string
	for {
		s = strings.TrimSpace(s)
		if s == "" {
			return out, nil
		}
		if s[0] != '"' {
			return nil, fmt.Errorf("expected quoted string")
		}
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return nil, fmt.Errorf("unterminated quote")
		}
		out = append(out, s[1:1+end])
		s = s[end+2:]
	}
}

// resolveVaultRef resolves one vault reference through the VaultRead hook
// (wired to the vaults registry at build).
func (r *Resolver) resolveVaultRef(ctx context.Context, name, key string) (string, error) {
	r.mu.Lock()
	read := r.VaultRead
	r.mu.Unlock()
	if read == nil {
		return "", fmt.Errorf("vault reference {{ vault %q %q }}: no vaults are configured (add a vaults: section)", name, key)
	}
	return read(ctx, name, key)
}
