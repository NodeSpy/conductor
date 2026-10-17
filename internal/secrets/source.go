package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// A value source is where one secret field's value comes from, written in
// the reference syntax conductor already uses elsewhere:
//
//	${env.NAME}                    the process environment (conductor.env included)
//	env:NAME                       the same, older spelling
//	{{ vault "<vault>" "<key>" }}  a vaults: entry ({{ .vaults.<vault>.<key> }} too)
//	{{secret "<vault>/<key>"}}     a named secret: the same vault entry, by its broker name
//	anything else                  a literal (typed) value
//
// `${settings.NAME}` is refused as a source: a setting is plain config,
// substituted into the file at load and visible to anything that reads
// it. A secret field that names one is a mistake worth a load error.
//
// ParseSource only classifies; the Resolver (or a caller with its own
// hooks, like the browser's fill-time resolution) reads the value.

// SourceKind is a value source's kind.
type SourceKind string

const (
	SourceLiteral SourceKind = "literal"
	SourceEnv     SourceKind = "env"
	SourceVault   SourceKind = "vault"
	// SourceSecret is a named secret ({{secret "vault/key"}}): a vault entry
	// named the way the broker and boundary handles name it.
	SourceSecret SourceKind = "secret"
)

// Source is one parsed value source.
type Source struct {
	Kind SourceKind
	// Env is the variable (SourceEnv).
	Env string
	// Vault / Key are the vault entry (SourceVault, SourceSecret).
	Vault, Key string
	// Literal is the typed value (SourceLiteral).
	Literal string
}

var (
	envSourceRE      = regexp.MustCompile(`^\$\{env\.([A-Za-z_][A-Za-z0-9_]*)\}$`)
	secretSourceRE   = regexp.MustCompile(`^\{\{-?\s*secret\s+"([^"]+)"\s*-?\}\}$`)
	settingsSourceRE = regexp.MustCompile(`\$\{settings\.[^}]*\}`)
	loaderEnvRE      = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)
)

// ErrPlainConfig is why a ${settings.NAME} is no secret source.
var ErrPlainConfig = fmt.Errorf("a ${settings.…} value is plain config (substituted into the file at load, readable by anything that reads it) — not a secret source; use a vault reference, ${env.NAME}, or a stored value")

// ParseSource classifies s. It errors for a source that can never hold a
// secret (${settings.…}) and for a malformed reference.
func ParseSource(s string) (Source, error) {
	t := strings.TrimSpace(s)
	if settingsSourceRE.MatchString(t) {
		return Source{}, ErrPlainConfig
	}
	if m := envSourceRE.FindStringSubmatch(t); m != nil {
		return Source{Kind: SourceEnv, Env: m[1]}, nil
	}
	if name, ok := strings.CutPrefix(t, "env:"); ok {
		if name == "" || strings.ContainsAny(name, " \t${}") {
			return Source{}, fmt.Errorf("env:%s is not an environment variable name", name)
		}
		return Source{Kind: SourceEnv, Env: name}, nil
	}
	if name, key, ok := ParseVaultRef(t); ok {
		return Source{Kind: SourceVault, Vault: name, Key: key}, nil
	}
	if m := secretSourceRE.FindStringSubmatch(t); m != nil {
		vault, key, ok := strings.Cut(m[1], "/")
		if !ok || vault == "" || key == "" {
			return Source{}, fmt.Errorf(`{{secret %q}}: a named secret is "<vault>/<key>"`, m[1])
		}
		return Source{Kind: SourceSecret, Vault: vault, Key: key}, nil
	}
	if strings.Contains(t, "{{") || strings.Contains(t, "${env.") {
		return Source{}, fmt.Errorf("%q is not one reference — a source is exactly ${env.NAME}, {{ vault \"<vault>\" \"<key>\" }}, {{secret \"<vault>/<key>\"}}, or a literal", clipRef(t))
	}
	if strings.HasPrefix(t, "op://") || strings.HasPrefix(t, "pass:") || strings.HasPrefix(t, "vault:") || strings.HasPrefix(t, "file:") {
		return Source{}, fmt.Errorf("the op://, pass:, vault: and file: schemes were retired — write {{ vault \"<vault>\" \"<key>\" }}")
	}
	return Source{Kind: SourceLiteral, Literal: s}, nil
}

// LoaderEnvRef reports whether s carries the loader's own ${NAME}
// expansion, which substitutes a value into the config at load: a secret
// field wants ${env.NAME}, read when the value is used.
func LoaderEnvRef(s string) bool { return loaderEnvRE.MatchString(s) }

// SettingsRef reports whether s carries a ${settings.…} reference.
func SettingsRef(s string) bool { return settingsSourceRE.MatchString(s) }

// String is the source in its canonical reference syntax. A literal is
// never spelled out: it renders as "(typed value)".
func (s Source) String() string {
	switch s.Kind {
	case SourceEnv:
		return "${env." + s.Env + "}"
	case SourceVault:
		return fmt.Sprintf("{{ vault %q %q }}", s.Vault, s.Key)
	case SourceSecret:
		return fmt.Sprintf("{{secret %q}}", s.Vault+"/"+s.Key)
	}
	return "(typed value)"
}

// IsRef reports whether the source names somewhere else (not a literal).
func (s Source) IsRef() bool { return s.Kind != SourceLiteral && s.Kind != "" }

func clipRef(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
