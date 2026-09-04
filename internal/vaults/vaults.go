// Package vaults is the vaults: framework: named, typed secret stores
// (conductor / onepassword / pass / file / hashicorp) behind one Backend
// contract, addressed by `{{ vault "<name>" "<key>" }}` references, the
// per-vault read/write verbs, and the `conductor vault <name>` CLI.
//
// Env is NOT a vault: `${VAR}` / `env:VAR` stays the implicit baseline that
// needs no declaration. A vault exists only once its `vaults:` entry is
// defined.
//
// Every value read through a vault is TAINTED: it is reported to the taint
// hook (wired to the secrets resolver's redaction list at build), so it is
// scrubbed from logs and the audit trail even after it flows into a later
// step's options. Values are cached in memory per backend, resolved at
// load/reload, and never written back to config.
package vaults

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Backend is one vault's read face — the one capability every type has.
// Implementations cache reads in memory for the process lifetime and report
// every value they return through Taint.
type Backend interface {
	Read(ctx context.Context, key string) (string, error)
}

// Writer is the optional write face. The conductor type (and any
// write-capable backend: pass, hashicorp) implements it; read-only backends
// (onepassword, file) do not — a write against them is a clear error, not a
// silent no-op.
type Writer interface {
	Write(ctx context.Context, key, value string) error
}

// Lister is the optional enumeration face (conductor, file).
type Lister interface {
	List(ctx context.Context) ([]string, error)
}

// Deleter is the optional removal face (writable backends).
type Deleter interface {
	Delete(ctx context.Context, key string) error
}

// taintMu guards the taint hook; taint is called on every read.
var (
	taintMu sync.RWMutex
	taintFn func(string)
)

// SetTaint wires the sensitive-value hook — the build points it at the
// secrets resolver's Track, so every vault read lands on the redaction list.
func SetTaint(fn func(string)) {
	taintMu.Lock()
	taintFn = fn
	taintMu.Unlock()
}

// Taint marks one value sensitive. Backends call it on every successful
// read (and writes taint the written value, which the caller already holds).
func Taint(v string) {
	taintMu.RLock()
	fn := taintFn
	taintMu.RUnlock()
	if fn != nil && v != "" {
		fn(v)
	}
}

// ---------------------------------------------------------------------------
// The vault registry — named vaults from the vaults: section, mirroring the
// kv/sqlstore registries. A vault whose unlock failed registers BROKEN (the
// reason recorded): the daemon boots, `secrets check` and the boot log name
// it, and only the steps/connectors that depend on it fail.
// ---------------------------------------------------------------------------

type entry struct {
	backend Backend
	typ     string
	broken  string // non-empty: the unlock/build failure
}

var (
	regMu sync.Mutex
	named = map[string]entry{}
)

// Register adds a named vault. Duplicates error.
func Register(name, typ string, b Backend) error {
	regMu.Lock()
	defer regMu.Unlock()
	if name == "" {
		return fmt.Errorf("vaults: empty vault name")
	}
	if _, dup := named[name]; dup {
		return fmt.Errorf("vaults: vault %q registered twice", name)
	}
	named[name] = entry{backend: b, typ: typ}
	return nil
}

// RegisterBroken records a vault that failed to unlock or build. Every use
// reports the reason; the daemon keeps booting.
func RegisterBroken(name, typ, reason string) error {
	regMu.Lock()
	defer regMu.Unlock()
	if name == "" {
		return fmt.Errorf("vaults: empty vault name")
	}
	if _, dup := named[name]; dup {
		return fmt.Errorf("vaults: vault %q registered twice", name)
	}
	named[name] = entry{typ: typ, broken: reason}
	return nil
}

// Reset clears every registered vault (config reload, tests).
func Reset() {
	regMu.Lock()
	defer regMu.Unlock()
	named = map[string]entry{}
}

// Names returns the registered vault names, sorted.
func Names() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(named))
	for n := range named {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Type returns a registered vault's type ("" when unknown).
func Type(name string) string {
	regMu.Lock()
	defer regMu.Unlock()
	return named[name].typ
}

// Broken returns the recorded failure for a vault that didn't unlock ("" when
// healthy or unknown).
func Broken(name string) string {
	regMu.Lock()
	defer regMu.Unlock()
	return named[name].broken
}

// Use resolves a vault selector to its backend. Every reference names a
// defined vault; there is no default. A broken vault resolves to its
// recorded unlock failure.
func Use(name string) (Backend, error) {
	if name == "" {
		return nil, fmt.Errorf("vaults: vault name is required (defined vaults: %s)", nameList())
	}
	regMu.Lock()
	e, ok := named[name]
	regMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("vaults: no vault named %q (defined vaults: %s)", name, nameList())
	}
	if e.broken != "" {
		return nil, fmt.Errorf("vaults: vault %q (%s) is unavailable: %s", name, e.typ, e.broken)
	}
	return e.backend, nil
}

// Read resolves and reads in one step — the shared path behind the template
// function, the resolver hook, and the verbs. The value is tainted.
func Read(ctx context.Context, name, key string) (string, error) {
	b, err := Use(name)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", fmt.Errorf("vault %q: key is required", name)
	}
	v, err := b.Read(ctx, key)
	if err != nil {
		return "", fmt.Errorf("vault %q: %w", name, err)
	}
	Taint(v)
	return v, nil
}

// Write resolves and writes in one step, erroring clearly on a read-only
// backend. The value is tainted.
func Write(ctx context.Context, name, key, value string) error {
	b, err := Use(name)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("vault %q: key is required", name)
	}
	w, ok := b.(Writer)
	if !ok {
		return fmt.Errorf("vault %q (%s) is read-only — writes need a writable type (conductor, pass, hashicorp)", name, Type(name))
	}
	if err := w.Write(ctx, key, value); err != nil {
		return fmt.Errorf("vault %q: %w", name, err)
	}
	Taint(value)
	return nil
}

func nameList() string {
	names := Names()
	if len(names) == 0 {
		return "none — add a vaults: section"
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Reference syntax. One form everywhere — config fields, step options,
// prompts: {{ vault "<name>" "<key>" }} (or the field form
// {{ .vaults.<name>.<key> }} for simple key names). ParseRef recognizes a
// string that IS exactly one such reference, for load-time resolution of
// config fields; inside larger templates the flow engine's `vault` function
// handles it.
// ---------------------------------------------------------------------------

// ParseRef reports whether s is exactly one vault reference and returns its
// vault name and key. Both spellings are recognized:
//
//	{{ vault "op" "Private/GitHub/token" }}
//	{{ .vaults.house.gh_token }}
func ParseRef(s string) (name, key string, ok bool) {
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

// IsRef reports whether s is exactly one vault reference.
func IsRef(s string) bool {
	_, _, ok := ParseRef(s)
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
