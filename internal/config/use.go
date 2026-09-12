package config

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// `use:` resolution — one kind-aware search path shared by `connectors:` and
// `runtimes:` (docs/design/use-unification.md §B).
//
// A `use:` reference answers exactly one question: WHAT IMPLEMENTS THIS? It
// replaces the old `plugins:` block plus `type:`/`source:`/`kind:`. The kind is
// never hand-authored — it is the block the reference appears in, cross-checked
// against the implementation's own self-description.
//
// First match wins:
//
//  1. bare name that is a BUILTIN of this kind  → in-binary implementation
//  2. bare name that is not builtin             → the OFFICIAL plugin repo,
//     component "<kind>s/<name>"
//  3. owner/repo[/component]                    → an explicit github.com repo
//  4. host.tld/owner/repo[//component]          → an explicit non-github host
//  5. ./p, ../p, /p, ~/p                        → a local executable (dev)
//
// Builtin beats official on a name clash; an explicit path is never a bare name
// so it never enters cases 1–2.

// UseKind is what a reference must resolve to — derived from the block the
// reference appears in, never written by hand.
type UseKind string

// UseKind values.
const (
	UseKindConnector UseKind = "connector"
	UseKindRuntime   UseKind = "runtime"
	// UseKindPack is a distributable config pack. Packs live in their own
	// official repo (they are config, not a binary), so a bare pack name
	// resolves to OfficialPacksRepo rather than the plugin repo.
	UseKindPack UseKind = "pack"
)

// Dir is the official repo's directory for this kind ("connectors", "runtimes")
// and the prefix on its release tags ("connectors/sentry/v1.0.0"). A pack sits
// at the root of its own repo, so its Dir is only the block name.
func (k UseKind) Dir() string {
	switch k {
	case UseKindRuntime:
		return "runtimes"
	case UseKindPack:
		return "packs"
	default:
		return "connectors"
	}
}

// officialRepoFor is the repo a bare, non-builtin name of this kind resolves
// to.
func (k UseKind) officialRepoFor() string {
	if k == UseKindPack {
		return OfficialPacksRepo
	}
	return OfficialRepo
}

// officialComponentFor is the path within the official repo. Plugins are laid
// out by kind (`connectors/sentry`); a pack is a top-level directory of the
// packs repo (`pr-review-team`).
func (k UseKind) officialComponentFor(name string) string {
	if k == UseKindPack {
		return name
	}
	return k.Dir() + "/" + name
}

// Block is the config block a reference of this kind appears in.
func (k UseKind) Block() string { return k.Dir() }

// UseOrigin classifies where a resolved reference comes from. It is what
// `plugin list` and `connectors ls` show, so an operator can see at a glance
// whether something is in-binary, official, or third-party.
type UseOrigin string

// UseOrigin values.
const (
	// OriginBuiltin is an implementation compiled into the daemon.
	OriginBuiltin UseOrigin = "builtin"
	// OriginOfficial is the official plugin repo (NodeSpy/conductor-plugins),
	// reached by a bare name that is not builtin.
	OriginOfficial UseOrigin = "official"
	// OriginGitHub is an explicitly-named github.com repo.
	OriginGitHub UseOrigin = "github"
	// OriginHost is an explicitly-named non-github host.
	OriginHost UseOrigin = "host"
	// OriginLocal is a local executable path (development).
	OriginLocal UseOrigin = "local"
)

// OfficialRepo is the plugin repo a bare, non-builtin name resolves to.
const OfficialRepo = "NodeSpy/conductor-plugins"

// OfficialPacksRepo is the pack repo a bare `packs:` key resolves to. Packs
// are config rather than binaries, so they have their own repo.
const OfficialPacksRepo = "NodeSpy/conductor-packs"

// OfficialPacksSource is the `pack_trust` source form of the official pack
// repo — in the DEFAULT allowlist, so naming an official pack needs no
// ceremony while a third-party source still does.
const OfficialPacksSource = "github.com/" + OfficialPacksRepo

// OfficialSource is the `plugin_trust` source form of the official repo. It is
// in the DEFAULT trust allowlist: adding an official plugin needs no ceremony,
// while a third-party repo still needs an explicit entry.
const OfficialSource = "github.com/" + OfficialRepo

// defaultHost is assumed whenever a remote reference names no host.
const defaultHost = "github.com"

// Use is a parsed, resolved `use:` reference.
type Use struct {
	// Raw is the reference exactly as written, for diagnostics.
	Raw string
	// Kind is the block the reference appeared in.
	Kind UseKind
	// Origin is where the implementation comes from.
	Origin UseOrigin
	// Name is the implementation's name: the builtin type for OriginBuiltin,
	// otherwise the last segment of the component (or the repo name when the
	// reference names no component).
	Name string
	// Host is the forge host for a remote reference ("github.com" unless the
	// reference named another).
	Host string
	// Repo is "owner/name" for a remote reference.
	Repo string
	// Component is the path within the repo ("connectors/sentry"). Empty for a
	// single-plugin repo.
	Component string
	// Version is the constraint from an `@…` suffix. Empty = track latest
	// compatible (stay-current).
	Version string
	// Path is the local executable path for OriginLocal, as written.
	Path string
}

// IsBuiltin reports whether the reference resolves to an in-binary implementation.
func (u Use) IsBuiltin() bool { return u.Origin == OriginBuiltin }

// IsRemote reports whether the reference must be fetched from a forge.
func (u Use) IsRemote() bool {
	switch u.Origin {
	case OriginOfficial, OriginGitHub, OriginHost:
		return true
	}
	return false
}

// IsPinned reports whether the version suffix is an EXACT version rather than a
// range. An exact pin is the opt-out from stay-current: `conductor init`,
// `plugin update`, and the auto-update cycle leave it where it is.
func (u Use) IsPinned() bool {
	v := strings.TrimPrefix(strings.TrimSpace(u.Version), "v")
	if v == "" {
		return false
	}
	// A range operator anywhere means "track within this range", not "pin".
	if strings.ContainsAny(v, "^~><=* ,") {
		return false
	}
	// Exactly major.minor.patch.
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return false
		}
	}
	return true
}

// Source is the canonical source string for the trust allowlist and for the
// install-state record: "<host>/<repo>[//<component>]". Empty for builtin and
// local references (neither is fetched, so neither is trust-gated).
func (u Use) Source() string {
	if !u.IsRemote() {
		return ""
	}
	s := u.Host + "/" + u.Repo
	if u.Component != "" {
		s += "//" + u.Component
	}
	return s
}

// TagPrefix is the component prefix on the repo's release tags — "" for a
// single-plugin repo, "connectors/sentry/" for the official monorepo. It is what
// BestMatch prefix-matches against.
func (u Use) TagPrefix() string {
	if u.Component == "" {
		return ""
	}
	return u.Component + "/"
}

// InstallKey identifies one installed plugin in the local install state:
// "<kind-dir>/<name>", e.g. "connectors/sentry".
func (u Use) InstallKey() string { return u.Kind.Dir() + "/" + u.Name }

// String renders the reference for logs and CLI output.
func (u Use) String() string {
	base := u.Raw
	if base == "" {
		base = u.Name
	}
	return base
}

// ParseUse resolves a `use:` reference for the given kind. It performs NO I/O:
// it decides where the implementation comes from, not whether it is installed.
func ParseUse(kind UseKind, ref string) (Use, error) {
	raw := strings.TrimSpace(ref)
	if raw == "" {
		return Use{}, fmt.Errorf("empty use: reference")
	}
	u := Use{Raw: raw, Kind: kind}

	body, version := splitUseVersion(raw)
	u.Version = version
	if body == "" {
		return Use{}, fmt.Errorf("use: %q has a version but no reference", raw)
	}

	// 5. Local executable path (development). Checked first: a path is never a
	// name, and a name never starts with a path prefix.
	if isLocalUsePath(body) {
		if version != "" {
			return Use{}, fmt.Errorf("use: %q: a local path cannot carry an @version — a local binary is whatever is on disk", raw)
		}
		u.Origin, u.Path = OriginLocal, body
		u.Name = localUseName(body)
		return u, nil
	}

	scheme := ""
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(body), p) {
			scheme, body = p, body[len(p):]
			break
		}
	}
	// A separator ANYWHERE (even a trailing one, which the trim below removes)
	// means the operator wrote a path, not a bare name.
	hadSep := strings.Contains(body, "/")
	body = strings.Trim(body, "/")
	if body == "" {
		return Use{}, fmt.Errorf("use: %q: no reference after the scheme", raw)
	}

	// The component separator "//" is accepted (it is what the old source:
	// field wrote) but no longer required — after owner/repo, everything left
	// is the component either way.
	hostPart, comp := body, ""
	if i := strings.Index(body, "//"); i >= 0 {
		hostPart, comp = body[:i], strings.Trim(body[i+2:], "/")
	}
	segs := splitSegments(hostPart)
	if len(segs) == 0 {
		return Use{}, fmt.Errorf("use: %q: empty reference", raw)
	}

	// A first segment containing a "." is a HOSTNAME; otherwise github.com is
	// implied. This is what separates `git.corp.example/team/repo//x` from
	// `acme/repo/x`.
	host := defaultHost
	if strings.Contains(segs[0], ".") {
		host, segs = segs[0], segs[1:]
	} else if scheme != "" {
		return Use{}, fmt.Errorf("use: %q: a %s reference must name a host", raw, strings.TrimSuffix(scheme, "://"))
	}

	// 1 & 2. A bare name: a single segment with no separator anywhere. A ref
	// that carried a "/" but collapsed to one segment ("acme/") is a malformed
	// repo, not a name — falling through to the official repo would quietly
	// fetch something the operator never asked for.
	if len(segs) == 1 && comp == "" && host == defaultHost && !hadSep &&
		!strings.Contains(segs[0], ".") {
		name := segs[0]
		if err := checkUseName(name); err != nil {
			return Use{}, fmt.Errorf("use: %q: %w", raw, err)
		}
		if builtinFor(kind, name) {
			if version != "" {
				return Use{}, fmt.Errorf("use: %q: %s is a builtin %s — it ships in the binary and has no version to pin (drop the @%s)", raw, name, kind, version)
			}
			u.Origin, u.Name = OriginBuiltin, name
			return u, nil
		}
		// Not builtin for THIS kind. If it is a builtin of the other kind, say
		// so plainly rather than sending the operator to the plugin repo for
		// something that is already in the binary.
		if other := otherKind(kind); other != "" && builtinFor(other, name) {
			return Use{}, fmt.Errorf("use: %q resolves to the builtin %s %q, but it is declared under %s: — a %s can never be wired as a %s", raw, other, name, kind.Block(), other, kind)
		}
		u.Origin = OriginOfficial
		u.Host, u.Repo = defaultHost, kind.officialRepoFor()
		u.Component = kind.officialComponentFor(name)
		u.Name = name
		return u, nil
	}

	// 3 & 4. An explicit repo. The first two segments are owner/repo; anything
	// after them is the component, whether it followed "/" or "//".
	if len(segs) < 2 {
		return Use{}, fmt.Errorf("use: %q: a remote reference needs owner/repo (got %q)", raw, strings.Join(segs, "/"))
	}
	if segs[0] == "" || segs[1] == "" {
		return Use{}, fmt.Errorf("use: %q: empty owner or repo", raw)
	}
	u.Host, u.Repo = host, segs[0]+"/"+segs[1]
	rest := segs[2:]
	if comp != "" {
		rest = append(rest, splitSegments(comp)...)
	}
	u.Component = strings.Join(rest, "/")
	if host == defaultHost {
		u.Origin = OriginGitHub
	} else {
		u.Origin = OriginHost
	}
	u.Name = segs[1]
	if len(rest) > 0 {
		u.Name = rest[len(rest)-1]
	}
	if err := checkUseName(u.Name); err != nil {
		return Use{}, fmt.Errorf("use: %q: %w", raw, err)
	}
	return u, nil
}

// splitUseVersion peels an "@<constraint>" suffix off the LAST path segment. A
// bare "@" elsewhere (an https://user@host reference) is left alone.
func splitUseVersion(ref string) (body, version string) {
	slash := strings.LastIndex(ref, "/")
	last := ref[slash+1:]
	at := strings.LastIndex(last, "@")
	if at < 0 {
		return ref, ""
	}
	v := strings.TrimSpace(last[at+1:])
	if v == "" || !looksLikeVersion(v) {
		return ref, ""
	}
	return ref[:slash+1+at], v
}

// looksLikeVersion reports whether s reads as a semver constraint rather than,
// say, the userinfo half of a URL.
func looksLikeVersion(s string) bool {
	switch s[0] {
	case 'v', 'V', '^', '~', '>', '<', '=', '*':
		return true
	}
	return s[0] >= '0' && s[0] <= '9'
}

// isLocalUsePath reports whether the reference is a filesystem path rather than
// a name or a repo.
func isLocalUsePath(s string) bool {
	return strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") || s == "." || s == ".."
}

// localUseName derives an implementation name from a local path, stripping the
// conventional "conductor-" binary prefix so `./bin/conductor-jira` is "jira".
func localUseName(p string) string {
	base := p
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimPrefix(base, "conductor-")
}

// splitSegments splits a slash path, dropping empty segments so "a//b" and
// "a/b/" behave.
func splitSegments(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "/") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkUseName rejects a name that could not be a directory component in the
// plugin repo or a connector type.
func checkUseName(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("invalid character %q in name %q (letters, digits, - _ . only)", r, name)
		}
	}
	return nil
}

// validateUseRef checks one `use:` field. legacyType carries a pre-`use:`
// `type:` value when the entry still has one, purely so a stale config gets a
// migration-specific error instead of an opaque "missing use:".
func validateUseRef(where, use, legacyType string, kind UseKind) error {
	u := strings.TrimSpace(use)
	if u == "" {
		if legacyType != "" {
			return fmt.Errorf("config: %s: `type: %s` was replaced by `use: %s` — the daemon migrates automatically at boot, or run `conductor config migrate`", where, legacyType, legacyType)
		}
		return fmt.Errorf("config: %s: missing use: — name what implements it (a builtin such as %s, a plugin name, or owner/repo/component)", where, strings.Join(firstN(BuiltinNames(kind), 3), " / "))
	}
	if legacyType != "" && legacyType != u {
		return fmt.Errorf("config: %s: both `use: %s` and the retired `type: %s` are set — delete the type: line", where, u, legacyType)
	}
	if _, err := ParseUse(kind, u); err != nil {
		return fmt.Errorf("config: %s: %w", where, err)
	}
	return nil
}

// validateEgressTarget checks one "host[:port]" (glob allowed in the host)
// declared-egress entry. It is deliberately shape-only — resolution and
// enforcement belong to the proxy, not the loader.
func validateEgressTarget(where, target string) error {
	t := strings.TrimSpace(target)
	if t == "" {
		return fmt.Errorf("config: %s: empty entry", where)
	}
	if strings.Contains(t, "://") || strings.Contains(t, "/") {
		return fmt.Errorf("config: %s: %q is a URL — declare a host or host:port target", where, target)
	}
	host, port, hasPort := strings.Cut(t, ":")
	if host == "" {
		return fmt.Errorf("config: %s: %q has no host", where, target)
	}
	if hasPort {
		if port == "" {
			return fmt.Errorf("config: %s: %q has an empty port", where, target)
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return fmt.Errorf("config: %s: %q has a non-numeric port", where, target)
			}
		}
	}
	return nil
}

func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// otherKind is the kind a name could be confused with. Only connectors and
// runtimes share the plugin repo and can be mis-declared for one another; a
// pack is config in its own repo, so it has no counterpart.
func otherKind(k UseKind) UseKind {
	switch k {
	case UseKindConnector:
		return UseKindRuntime
	case UseKindRuntime:
		return UseKindConnector
	}
	return ""
}

// --- the builtin registries the search path consults -------------------------

// builtinRuntimes are the runtimes compiled into the daemon. Unlike connectors
// there is no plugin-style registry to consult, so the list is here.
var builtinRuntimes = map[string]bool{
	"paseo":      true,
	"acp":        true,
	"opencode":   true,
	"agent-deck": true,
	"cli":        true,
}

// builtinConnectors are the connector types compiled into the daemon. The list
// is SEEDED with the bundled types so a config-only build (no internal/connector
// linked in — every test in this package) resolves them, and internal/connector's
// RegisterType adds to it at init so a newly-bundled type cannot drift out of
// sync. External (plugin-backed) types are deliberately NOT registered here:
// they are what `use:` fetches, not what it short-circuits.
var (
	builtinMu         sync.RWMutex
	builtinConnectors = map[string]bool{
		"blob": true, "command": true, "conductor": true, "cron": true,
		"discord": true, "github": true, "graphql": true, "kv": true,
		"memory": true, "rest": true, "rss": true, "slack": true,
		"sql": true, "web": true, "webhook": true, "workflow": true,
	}
)

// RegisterBuiltinConnector records a connector type as bundled, so a bare `use:`
// naming it resolves in-binary instead of reaching for the plugin repo. Called
// from internal/connector.RegisterType at init.
func RegisterBuiltinConnector(name string) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	builtinConnectors[name] = true
}

// BuiltinConnector reports whether a name is a bundled connector type.
func BuiltinConnector(name string) bool {
	builtinMu.RLock()
	defer builtinMu.RUnlock()
	return builtinConnectors[name]
}

// BuiltinRuntime reports whether a name is a bundled runtime.
func BuiltinRuntime(name string) bool { return builtinRuntimes[name] }

// BuiltinNames lists the bundled implementations of a kind, sorted — for error
// messages and `plugin list`.
func BuiltinNames(kind UseKind) []string {
	var out []string
	if kind == UseKindRuntime {
		for n := range builtinRuntimes {
			out = append(out, n)
		}
	} else {
		builtinMu.RLock()
		for n := range builtinConnectors {
			out = append(out, n)
		}
		builtinMu.RUnlock()
	}
	sort.Strings(out)
	return out
}

func builtinFor(kind UseKind, name string) bool {
	if kind == UseKindRuntime {
		return BuiltinRuntime(name)
	}
	return BuiltinConnector(name)
}
