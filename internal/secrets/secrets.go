// Package secrets resolves secret references used in conductor config.
//
// A secret reference is a scheme-prefixed string naming where the value lives:
//
//	env:GH_PAT              process environment / conductor.env (the baseline)
//	op://Vault/Item/field   1Password (`op read`; Service Account or Connect)
//	pass:conductor/gh       pass, the GPG password store (`pass show`)
//	vault:gh-token          conductor's built-in encrypted vault (see vault.go)
//	file:/run/secrets/gh    a mounted file (systemd/docker/k8s)
//
// Anything that is not a reference passes through Resolve unchanged, so config
// fields can hold either a literal value or a reference. Resolved values are
// cached in memory for the process lifetime, redacted from logs/audit via
// Redact, and never written back to disk.
package secrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// execTimeout bounds one external helper invocation (`op read`, `pass show`) so
// a hung helper can't stall config load forever.
const execTimeout = 30 * time.Second

// Resolver resolves secret references. The zero value is not usable; call New.
// All lookups are injectable for tests.
type Resolver struct {
	// LookupEnv resolves env: references (default os.LookupEnv).
	LookupEnv func(string) (string, bool)
	// ReadFile resolves file: references and reads the vault (default os.ReadFile).
	ReadFile func(string) ([]byte, error)
	// Exec runs an external secret helper (`op`, `pass`) and returns its stdout.
	// The default execs the command with a bounded timeout.
	Exec func(ctx context.Context, name string, args ...string) (string, error)
	// VaultPath is the encrypted vault file for vault: references (default
	// DefaultVaultPath()).
	VaultPath string
	// VaultKey returns the vault master key material (default: the KeyChain
	// lookup order documented in vault.go).
	VaultKey func() ([]byte, error)

	mu     sync.Mutex
	cache  map[string]string // ref -> resolved value
	values []string          // resolved values, longest first, for redaction
}

// New returns a Resolver with the default OS-backed lookups.
func New() *Resolver {
	return &Resolver{
		LookupEnv: os.LookupEnv,
		ReadFile:  os.ReadFile,
		Exec:      execHelper,
		VaultPath: DefaultVaultPath(),
		cache:     map[string]string{},
	}
}

// execHelper runs an external helper and returns trimmed stdout.
func execHelper(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// IsRef reports whether s is a secret reference (as opposed to a literal).
func IsRef(s string) bool {
	switch {
	case strings.HasPrefix(s, "env:"),
		strings.HasPrefix(s, "op://"),
		strings.HasPrefix(s, "pass:"),
		strings.HasPrefix(s, "vault:"),
		strings.HasPrefix(s, "file:"):
		return true
	}
	return false
}

// Resolve returns the value a reference names. A non-reference string is
// returned as-is (it is a literal, and is NOT registered for redaction —
// callers register literals they know are secret via Track). Results are
// cached; a second Resolve of the same ref never re-runs the helper.
func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	if !IsRef(ref) {
		return ref, nil
	}
	r.mu.Lock()
	if v, ok := r.cache[ref]; ok {
		r.mu.Unlock()
		return v, nil
	}
	r.mu.Unlock()

	v, err := r.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]string{}
	}
	r.cache[ref] = v
	r.trackLocked(v)
	r.mu.Unlock()
	return v, nil
}

func (r *Resolver) resolve(ctx context.Context, ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		if name == "" {
			return "", fmt.Errorf("secret %q: empty variable name", ref)
		}
		lookup := r.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}
		v, ok := lookup(name)
		if !ok {
			return "", fmt.Errorf("secret %q: environment variable %s is not set", ref, name)
		}
		return v, nil
	case strings.HasPrefix(ref, "op://"):
		return r.exec(ctx, "op", "read", "-n", ref)
	case strings.HasPrefix(ref, "pass:"):
		name := strings.TrimPrefix(ref, "pass:")
		if name == "" {
			return "", fmt.Errorf("secret %q: empty pass entry name", ref)
		}
		out, err := r.exec(ctx, "pass", "show", name)
		if err != nil {
			return "", err
		}
		// pass entries may carry extra lines (metadata); the secret is line one.
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[:i]
		}
		return out, nil
	case strings.HasPrefix(ref, "vault:"):
		name := strings.TrimPrefix(ref, "vault:")
		if name == "" {
			return "", fmt.Errorf("secret %q: empty vault entry name", ref)
		}
		return r.vaultGet(name)
	case strings.HasPrefix(ref, "file:"):
		path := strings.TrimPrefix(ref, "file:")
		if path == "" {
			return "", fmt.Errorf("secret %q: empty file path", ref)
		}
		read := r.ReadFile
		if read == nil {
			read = os.ReadFile
		}
		b, err := read(expandHome(path))
		if err != nil {
			return "", fmt.Errorf("secret %q: %w", ref, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return "", fmt.Errorf("secret %q: unknown scheme", ref)
}

func (r *Resolver) exec(ctx context.Context, name string, args ...string) (string, error) {
	run := r.Exec
	if run == nil {
		run = execHelper
	}
	return run(ctx, name, args...)
}

// Track registers a known-secret literal value (e.g. a token that arrived via
// ${ENV} expansion) so Redact scrubs it even though it wasn't Resolve'd here.
func (r *Resolver) Track(value string) {
	r.mu.Lock()
	r.trackLocked(value)
	r.mu.Unlock()
}

// trackLocked adds a value to the redaction list (longest first so an
// overlapping shorter value doesn't split a longer one mid-replace). Values
// shorter than 4 bytes are not tracked — replacing them would shred ordinary
// text far more than it would protect anything.
func (r *Resolver) trackLocked(v string) {
	if len(v) < 4 {
		return
	}
	for _, have := range r.values {
		if have == v {
			return
		}
	}
	r.values = append(r.values, v)
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// Redact replaces every tracked secret value in s with a placeholder.
func (r *Resolver) Redact(s string) string {
	r.mu.Lock()
	vals := append([]string(nil), r.values...)
	r.mu.Unlock()
	for _, v := range vals {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Placeholder)
		}
	}
	return s
}

// RedactValue redacts a template-data value in place-compatible fashion:
// strings are Redact'ed, maps and slices are walked, everything else passes
// through. Used to scrub audit entries (e.g. verb options) before writing.
func (r *Resolver) RedactValue(v any) any {
	switch x := v.(type) {
	case string:
		return r.Redact(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.RedactValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.RedactValue(e)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = r.Redact(e)
		}
		return out
	}
	return v
}

// Placeholder is what a redacted secret renders as.
const Placeholder = "«redacted»"

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

// StoreVault writes name=value into the resolver's configured vault and
// refreshes the in-memory cache for the matching vault: reference — the
// oauth2 refresh-token rotation write-back, so a provider-rotated token
// survives the daemon's own restart. The value is tracked for redaction.
func (r *Resolver) StoreVault(name, value string) error {
	v, err := OpenVault(r.VaultPath, r.VaultKey)
	if err != nil {
		return err
	}
	if err := v.Set(name, value); err != nil {
		return err
	}
	if err := v.Save(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]string{}
	}
	r.cache["vault:"+name] = value
	r.trackLocked(value)
	r.mu.Unlock()
	return nil
}
