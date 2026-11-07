package plugin

import (
	"net/url"
	"slices"

	"crypto/sha256"
	"encoding/hex"
	"fmt"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// Local install state — the app-extension model's answer to "which build of
// this plugin is on this box?" (docs/design/use-unification.md §C).
//
// This is deliberately NOT a committed lockfile. A pack is config, and config
// belongs in the repo; a plugin is an INSTALLED BINARY, and which binary is
// installed is a property of this machine. So install state lives in the state
// dir next to the daemon's other local data:
//
//	~/.local/state/conductor/plugins/
//	  installed.yaml
//	  connectors/widget/conductor-widget_linux_amd64
//	  runtimes/paseo/conductor-paseo_linux_amd64
//
// Boot reads it OFFLINE. Nothing on the hot path touches the network: a fetch
// happens only for a genuine gap (referenced, not installed), and a fetch that
// fails leaves what is already installed running.

// installStateFile is the state file's name inside the plugin install dir.
const installStateFile = "installed.yaml"

// installStateVersion is bumped only on an incompatible format change; an
// unknown version is treated as "no state" rather than an error, so a downgrade
// re-installs instead of crash-looping.
//
// Version 2 is the file the plugin jail writes: every record in it holds
// what the user agreed to. A version 1 file was written by a conductor that
// did not jail plugins (or by one rolled back to): its records are loaded
// as Legacy, and only those are ever adopted (adoptLegacyRecord).
const installStateVersion = 2

// legacyStateVersion is the file a conductor without the plugin jail writes.
const legacyStateVersion = 1

// consentFile keeps a copy of the last version 2 state beside the state
// file. A conductor without the jail rewrites installed.yaml as version 1,
// dropping what it does not know (manifest_schema, pending); it never
// touches this file, so after a rollback and upgrade the adoption compares
// against what the user last agreed to, and held updates are restored.
const consentFile = "consented.yaml"

// manifestSchema marks a record whose Manifest holds the whole declaration
// the jail enforces (Installed.ManifestSchema). A record without it was
// written by a conductor that did not jail plugins and did not record
// listen, read, write, exec or keychain (Installed.Legacy).
const manifestSchema = 1

// Manifest is the permission manifest a plugin declared at install time — what
// it says it needs, recorded so it is known BEFORE the plugin runs on any
// later boot, and so a change on update is visible rather than silent.
type Manifest struct {
	// Egress are the destinations the plugin connects to (plugin.Capabilities).
	Egress []string `yaml:"egress,omitempty"`
	// Listen are the addresses it serves on.
	Listen []string `yaml:"listen,omitempty"`
	// Read and Write are the paths it reads, and reads and writes.
	Read  []string `yaml:"read,omitempty"`
	Write []string `yaml:"write,omitempty"`
	// Commands are the commands it spawns.
	Commands []string `yaml:"commands,omitempty"`
	// Exec are the paths whose programs it runs beyond Commands.
	Exec []string `yaml:"exec,omitempty"`
	// Keychain: a command reads a login from the macOS Keychain.
	Keychain bool `yaml:"keychain,omitempty"`
	// FS is the older name for Write.
	FS []string `yaml:"fs,omitempty"`
	// Spawns records the legacy boolean for a plugin that declares it spawns
	// children without naming them.
	Spawns bool `yaml:"spawns,omitempty"`
	// Env are the daemon environment variables passed through to it.
	Env []string `yaml:"env,omitempty"`
	// Auth records the plugin's declared OAuth2 endpoints (Decl.Auth) so
	// `conductor connector auth <name>` can run the one-time interactive login
	// from the CLI without respawning the plugin to re-Describe it. nil for a
	// plugin that declares no managed auth.
	Auth *AuthSpec `yaml:"auth,omitempty"`
	// SingleProcess records the plugin's declared Capabilities.SingleProcess
	// (pkg/plugin/wire.go): every configured instance of it must share ONE
	// process, a box-global resource it keeps forces this regardless of the
	// operator's own isolate: setting — isolate: true on any instance of it
	// is refused rather than honored (cmd/conductor's loadConnectorPlugins,
	// Manager.ForbidIsolated). Recorded at install time, like every other
	// capability, so it is known on a LATER boot before the plugin ever
	// runs — checked as a hard config validation error against isolate:
	// true BEFORE anything spawns, rather than only at the live describe
	// that first discovers it (which instead disables just the isolated
	// instance, since the conflict was not knowable any earlier that time).
	SingleProcess bool `yaml:"single_process,omitempty"`
}

// IsZero reports whether the plugin declared no capabilities at all.
func (m Manifest) IsZero() bool {
	return len(m.Egress) == 0 && len(m.Listen) == 0 && len(m.Read) == 0 && len(m.Write) == 0 && len(m.Exec) == 0 &&
		len(m.Commands) == 0 && len(m.FS) == 0 && !m.Spawns && !m.Keychain && len(m.Env) == 0 && !m.SingleProcess && m.Auth == nil
}

// Caps is the manifest as the plugin declared it.
func (m Manifest) Caps() sdk.Capabilities {
	return sdk.Capabilities{
		Egress: m.Egress, Listen: m.Listen, Read: m.Read, Write: m.Write, FS: m.FS,
		Commands: m.Commands, Exec: m.Exec, Keychain: m.Keychain, Env: m.Env,
	}
}

// Lines renders what the plugin may do, one permission per line, in plain
// words: what `plugin add`, `plugin show` and the boot log show the user.
func (m Manifest) Lines() []string {
	var out []string
	add := func(label string, entries []string) {
		if len(entries) == 0 {
			return
		}
		var ws []string
		for _, e := range entries {
			ws = append(ws, plainEntry(e))
		}
		out = append(out, label+": "+strings.Join(ws, ", "))
	}
	add("connects to", m.Egress)
	add("listens on", m.Listen)
	add("reads", m.Read)
	var files, sockets []string
	for _, e := range append(append([]string(nil), m.Write...), m.FS...) {
		if socketEntry(e) {
			sockets = append(sockets, e)
		} else {
			files = append(files, e)
		}
	}
	add("reads and writes", files)
	add(socketLabel, sockets)
	add("runs", m.Commands)
	add("runs programs in", m.Exec)
	if len(m.Read)+len(m.Write)+len(m.FS)+len(m.Exec) > 0 {
		out = append(out, "note: a directory it may read, write or run programs in includes connecting to any socket in it")
	}
	add("reads env", m.Env)
	if secret := secretLooking(m.Env); len(secret) > 0 {
		out = append(out, "note: "+strings.Join(secret, ", ")+" look like credentials — it gets their values from conductor's environment when they are set there")
	}
	if m.Keychain {
		out = append(out, "reads a login from the macOS Keychain")
	}
	if hosts := authEndpoints(m.Auth); len(hosts) > 0 {
		var ws []string
		for _, e := range hosts {
			ws = append(ws, e.where)
		}
		out = append(out, "conductor sends your OAuth client credentials and tokens to: "+strings.Join(uniqStrings(ws), ", "))
	}
	if m.SingleProcess {
		out = append(out, "single_process (every instance shares one process)")
	}
	if len(out) == 0 {
		out = []string{"nothing: no network, no files beyond its own, no commands, no env"}
	}
	return out
}

// withReferences is have plus the config: and env: entries of declared
// (optional ones included) that it lacks.
func withReferences(have, declared []string) []string {
	out := have
	for _, e := range declared {
		v := strings.TrimPrefix(e, sdk.OptionalPrefix)
		if (strings.HasPrefix(v, "config:") || strings.HasPrefix(v, "env:")) && !slices.Contains(out, e) {
			out = append(slices.Clip(out), e)
		}
	}
	return out
}

// plainEntry spells out a reference: "config:url" is the value of the
// instance's url field, "env:X/sub" a path under $X.
func plainEntry(e string) string {
	if ref, ok := strings.CutPrefix(e, sdk.OptionalPrefix); ok {
		return plainEntry(ref) + " (if it can be granted)"
	}
	if ref, ok := strings.CutPrefix(e, "config:"); ok {
		return "<" + shown(ref) + " from its config>"
	}
	if ref, ok := strings.CutPrefix(e, "env:"); ok {
		return "$" + shown(ref)
	}
	return shown(e)
}

// socketLabel is how consent names a write grant inside a runtime dir: the
// one thing granted there is a socket file (grantChecks.refusal).
const socketLabel = "talks to the program serving the socket"

// socketEntry: e names a path inside a runtime dir (runtimeDirs), or under
// $XDG_RUNTIME_DIR.
func socketEntry(e string) bool {
	p := strings.TrimPrefix(e, sdk.OptionalPrefix)
	if ref, ok := strings.CutPrefix(p, "env:"); ok {
		v, _, _ := strings.Cut(ref, "/")
		return v == "XDG_RUNTIME_DIR"
	}
	if !filepath.IsAbs(p) || grantExempt(p) {
		return false
	}
	for _, d := range runtimeDirs() {
		if within(d, p) && filepath.Clean(p) != d {
			return true
		}
	}
	return false
}

// Summary renders the manifest as one line for logs and install review.
func (m Manifest) Summary() string {
	if m.IsZero() {
		return "nothing (no network, no files beyond its own, no commands, no env)"
	}
	return strings.Join(m.Lines(), "; ")
}

// Widens lists what next declares that m does not: each entry is a new
// permission the user has not agreed to. Empty means next is the same or
// narrower. A destination already covered by one of m's globs is not new.
func (m Manifest) Widens(next Manifest) []string {
	var out []string
	diff := func(label string, have, want []string, covered func(have []string, e string) bool) {
		for _, e := range want {
			if !covered(have, e) {
				out = append(out, label+" "+plainEntry(e))
			}
		}
	}
	exact := func(have []string, e string) bool { return slices.Contains(have, e) }
	// A destination compares as the jail grants it: a bare host is :443 on
	// both sides, so "api.example.com" never covers another port.
	diff("connects to", m.Egress, next.Egress, func(have []string, e string) bool {
		return exact(have, e) || (!strings.HasPrefix(e, "config:") && egressCovered(have, e))
	})
	diff("listens on", m.Listen, next.Listen, exact)
	// A path m may write, it may also read.
	// An optional path ("?…") is covered by the same path, required.
	path := func(have []string, e string) bool {
		return exact(have, e) || exact(have, strings.TrimPrefix(e, sdk.OptionalPrefix))
	}
	diff("reads", append(append(append([]string(nil), m.Read...), m.Write...), m.FS...), next.Read, path)
	var nextFiles, nextSockets []string
	for _, e := range append(append([]string(nil), next.Write...), next.FS...) {
		if socketEntry(e) {
			nextSockets = append(nextSockets, e)
		} else {
			nextFiles = append(nextFiles, e)
		}
	}
	diff("writes", append(append([]string(nil), m.Write...), m.FS...), nextFiles, path)
	diff(socketLabel, append(append([]string(nil), m.Write...), m.FS...), nextSockets, path)
	diff("runs", m.Commands, next.Commands, exact)
	diff("runs programs in", m.Exec, next.Exec, path)
	diff("reads env", m.Env, next.Env, exact)
	if next.Keychain && !m.Keychain {
		out = append(out, "reads a login from the macOS Keychain")
	}
	// One process serving every instance is jailed to all of their grants.
	if next.SingleProcess && !m.SingleProcess {
		out = append(out, "single_process (every instance shares one process)")
	}
	// The OAuth endpoints are where conductor itself, outside any jail,
	// posts the operator's client secret and refresh token: one that moves
	// to another place is a new destination for them.
	have := map[string]bool{}
	for _, e := range authEndpoints(m.Auth) {
		have[e.field+" "+e.where] = true
	}
	for _, e := range authEndpoints(next.Auth) {
		if !have[e.field+" "+e.where] {
			out = append(out, "sends OAuth credentials to "+e.where+" ("+e.field+")")
		}
	}
	return out
}

// authEndpoint is one declared OAuth endpoint: which one, and where it
// sends to (scheme://host[:port]).
type authEndpoint struct{ field, where string }

func authEndpoints(a *AuthSpec) []authEndpoint {
	if a == nil {
		return nil
	}
	var out []authEndpoint
	for _, e := range []struct{ field, url string }{
		{"token_url", a.TokenURL}, {"auth_url", a.AuthURL}, {"device_auth_url", a.DeviceAuthURL},
	} {
		if strings.TrimSpace(e.url) == "" {
			continue
		}
		where := strings.TrimSpace(e.url)
		if u, err := url.Parse(where); err == nil && u.Host != "" {
			where = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
		}
		out = append(out, authEndpoint{e.field, shown(where)})
	}
	return out
}

// secretLooking lists the variable names that look like credentials, so the
// user sees that agreeing to them hands the plugin a secret.
func secretLooking(names []string) []string {
	var out []string
	for _, n := range names {
		u := strings.ToUpper(n)
		for _, w := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "PRIVATE_KEY", "ACCESS_KEY", "CREDENTIAL", "AUTH_SOCK"} {
			if strings.Contains(u, w) {
				out = append(out, shown(n))
				break
			}
		}
	}
	return out
}

func uniqStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Installed is one installed plugin's local record.
type Installed struct {
	// Key is "<kind-dir>/<name>" — "connectors/widget".
	Key string `yaml:"key"`
	// Kind is connector | runtime.
	Kind string `yaml:"kind"`
	// Name is the implementation name (connector type / runtime name).
	Name string `yaml:"name"`
	// Use is the reference as written in the config, so a changed reference is
	// detectable without re-resolving.
	Use string `yaml:"use"`
	// Source is the canonical fetch source ("host.example/o/r//comp").
	Source string `yaml:"source,omitempty"`
	// Resolved is the concrete release tag this build came from.
	Resolved string `yaml:"resolved,omitempty"`
	// Sha256 is the verified sha of the installed binary. Remembering it is
	// what makes a surprise change on update visible.
	Sha256 string `yaml:"sha256,omitempty"`
	// Path is the absolute path to the installed binary.
	Path string `yaml:"path"`
	// Manifest is the permission manifest recorded at install.
	Manifest Manifest `yaml:"manifest,omitempty"`
	// ManifestSchema is manifestSchema when Manifest was recorded in full
	// (see there); 0 on a record a conductor without the jail wrote.
	ManifestSchema int `yaml:"manifest_schema,omitempty"`
	// Legacy marks a record loaded from a version 1 state file (a conductor
	// without the jail wrote it) that has not been adopted yet. Only such a
	// record is adopted (adoptLegacyRecord); conductor never sets it on a
	// record it makes, so a record it wrote is always compared, never
	// adopted.
	Legacy bool `yaml:"legacy,omitempty"`
	// PreJail marks a plugin (this key from this source) installed before
	// the plugin jail: by a conductor without it, or recorded by one with it
	// that could not adopt it yet. Every build of the plugin carries it, so
	// it outlives updates. On a host that cannot jail plugins such a plugin
	// runs as it did before the jail (runsAsBefore); a host that can jails
	// it like any other.
	PreJail bool `yaml:"pre_jail,omitempty"`
	// ReleaseVerified records that Sha256 was VERIFIED against the release at
	// install — the release's checksums.txt listed the asset with this sha —
	// rather than merely computed from whatever was downloaded. Absent on
	// records written before the field existed and on releases that publish no
	// checksums: those still run (Sha256 is checked before every exec), but
	// get nothing that is granted on the strength of a verified release.
	ReleaseVerified bool `yaml:"release_verified,omitempty"`
}

// InstallState is the whole local install record, loaded from and saved to the
// state dir. A nil *InstallState is a valid empty state (every read is
// nil-safe), so callers with no state dir need no special case.
type InstallState struct {
	Version int         `yaml:"version"`
	Plugins []Installed `yaml:"plugins,omitempty"`
	// Pending are updates held because they declare more than the version
	// they would replace: installed on disk, not run, until the user
	// approves them (approve.go).
	Pending []Pending `yaml:"pending,omitempty"`

	dir string `yaml:"-"`
	// consented is the last version 2 state (consentFile), loaded when the
	// state file is version 1: what the user agreed to before a rollback.
	consented *InstallState
}

// InstallDir is where plugin binaries and install state live:
// <state-dir>/plugins. Empty when the state dir cannot be determined.
func InstallDir() string {
	sd := config.StateDir()
	if sd == "" {
		return ""
	}
	return filepath.Join(sd, "plugins")
}

// BinDirFor is a plugin KEY's installation directory: <install-dir>/<key> —
// the parent of every version's own subdirectory (BinDirForVersion). `plugin
// remove` removes it wholesale, taking every installed version with it.
func BinDirFor(dir, key string) string { return filepath.Join(dir, filepath.FromSlash(key)) }

// BinDirForVersion is where ONE resolved version of a plugin, from ONE
// source, is installed: <install-dir>/<key>/<version>[@<source-fingerprint>].
// Side by side versions need their own directory each — two versions of a
// plugin publish the SAME per-platform asset filename (RemoteSource.AssetName),
// so fetching a second version into the bare key directory would silently
// overwrite the first version's binary on disk out from under any instance
// still pinned to it. resolved is sanitized to a single path segment (no
// separators, injective — see sanitizeVersionDir) so a monorepo's
// component-prefixed tag ("connectors/widget/v1.2.3") can't escape the key's
// own directory.
//
// source is folded in too (finding 2, HIGH): nothing in this package itself
// stops two DIFFERENT sources (repos) under one install key from resolving
// to the identical tag TEXT — config.validatePluginRefs refuses that within
// one config, but that check does not survive a sequential config change,
// and install state persists across those. Without source in the directory
// name, two such sources would race to write the SAME file at the SAME
// path, and whichever fetch ran last would silently overwrite the other's
// binary out from under its own, still-accurate install-state record (same
// Key+Resolved, different Sha256). source may be "" for a caller that does
// not distinguish by source (every existing single-source call site, and
// every pre-existing on-disk layout, since "" contributes no suffix at
// all) — that is deliberately indistinguishable from "source not part of
// this lookup", never a wildcard that could alias a real source onto it.
func BinDirForVersion(dir, key, resolved, source string) string {
	return filepath.Join(BinDirFor(dir, key), versionDirSegment(resolved, source))
}

// versionDirSegment is BinDirForVersion's own directory name: the sanitized
// tag, plus — when source is known — a short stable fingerprint of it, so
// two sources that happen to tag the identical release text under one key
// never share a directory. The fingerprint is a prefix of sha256(source)
// rather than a sanitized form of source itself: source is an operator- and
// forge-controlled string (host/repo/component) with no length limit and no
// guarantee against separators or other unsafe bytes, and hashing sidesteps
// needing a second injective filesystem encoding for it.
func versionDirSegment(resolved, source string) string {
	seg := sanitizeVersionDir(resolved)
	if source == "" {
		return seg
	}
	return seg + "@" + sourceFingerprint(source)
}

// sourceFingerprint is a short, stable, collision-resistant identifier for a
// plugin source string, used only to keep two different sources' version
// directories apart (versionDirSegment) — never compared for security
// purposes, so a short sha256 prefix is a deliberate, ample margin against
// accidental collision without the directory name needing to be long.
func sourceFingerprint(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])[:12]
}

// sanitizeVersionDir maps a resolved version/tag to an INJECTIVE,
// filesystem-safe single path segment: two DISTINCT inputs always produce
// two DISTINCT outputs.
//
// Finding (HIGH, security/correctness): an earlier version of this function
// just replaced "/" and "\\" with "_", which is not injective — "a/b/widget/
// v1.0.0" and "a_b/widget/v1.0.0" both collapse to "a_b_widget_v1.0.0". Two
// configured instances resolving against those two component paths would
// then share ONE on-disk directory: a config migration moving one off an
// old source onto the other makes GCVersions RemoveAll the very directory
// the other version was just installed into (its sibling's binary vanishes
// as collateral damage of an unrelated prune).
//
// The fix is percent-encoding (RFC 3986 "unreserved" alphabet kept literal,
// everything else escaped as upper-case "%XX"), which IS injective by
// construction: '%' is itself always escaped (to "%25"), so '%' can never
// appear in the output except as the first byte of a 3-byte escape — the
// output is therefore uniquely decodable left-to-right (and unique
// decodability implies two different inputs can never produce the same
// output). This is a straightforward per-byte scan, so there is no
// "percent-encode '%' first, then …" ORDERING to get wrong the way
// sequential strings.ReplaceAll calls would (each pass risks re-escaping a
// '%' introduced by an earlier one); scanning once and classifying each
// byte as safe/unsafe sidesteps that class of bug entirely.
//
// '.' is kept literal (safe) for readability — "v1.0.0" passes straight
// through — EXCEPT when the ENTIRE value is exactly "." or ".." (never a
// real release tag; already evidence of a corrupt or tampered record), in
// which case it is escaped to "%2E"/"%2E%2E" so BinDirForVersion can never
// resolve to the plugin's own key directory or its parent. No other input's
// generic encoding can ever produce "%2E...": a literal '%' in any OTHER
// input is always escaped to "%25" by the per-byte scan below, so "%2E" is
// never ambiguous with it.
//
// Mirrors the same "reject the special entries, not just separators" rule
// Client.StagingDir applies to a leading dot. The directory LAYOUT itself is
// new on this unreleased branch, so no on-disk migration of old
// underscore-joined directories is needed — but the two encodings must
// never be mistaken for one another, and in practice they never collide:
// the old encoding only ever produced plain "_"-joined text, while this one
// emits "%XX" escapes for every byte the old encoding passed through
// unexamined (any '/', '\\', or '%' in the original value).
// maxVersionDirSegment caps sanitizeVersionDir's output length (finding 4,
// LOW): an arbitrarily long release tag (a monorepo path abused as a tag,
// or simply a very long one) would otherwise produce an arbitrarily long
// directory name — most filesystems cap a single path component around 255
// bytes, and percent-encoding can triple the length of every unsafe byte.
// Past this, the segment is replaced with a short, still-readable prefix
// plus a hash of the FULL ORIGINAL value (truncateWithHash) — never just a
// truncated prefix on its own, which would collapse two distinct long tags
// sharing one onto the same directory.
const maxVersionDirSegment = 150

func sanitizeVersionDir(v string) string {
	var out string
	if v == "." || v == ".." {
		out = strings.ReplaceAll(v, ".", "%2E")
	} else {
		var b strings.Builder
		b.Grow(len(v))
		for i := 0; i < len(v); i++ {
			c := v[i]
			if isSafeVersionDirByte(c) {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
		out = b.String()
		if len(out) > maxVersionDirSegment {
			out = truncateWithHash(out, v)
		}
	}
	return escapeWindowsReservedStem(out)
}

// truncateWithHash shortens encoded (already over maxVersionDirSegment) to a
// readable prefix of itself plus a short hash of original — the UNTRUNCATED
// source value, not the encoded prefix — so two distinct long inputs that
// happen to share their first bytes (a common case: two tags differing only
// near the end) still get distinct directory names. Collision-resistant,
// not collision-proof, the same trade-off sourceFingerprint already makes
// for exactly the same reason (never compared for security purposes — only
// to keep two unrelated directories apart).
func truncateWithHash(encoded, original string) string {
	const hashLen = 12 // matches sourceFingerprint's own length
	sum := sha256.Sum256([]byte(original))
	prefixLen := maxVersionDirSegment - 1 - hashLen // 1 for the "~" separator
	if prefixLen > len(encoded) {
		prefixLen = len(encoded)
	}
	if prefixLen < 0 {
		prefixLen = 0
	}
	return encoded[:prefixLen] + "~" + hex.EncodeToString(sum[:])[:hashLen]
}

// windowsReservedStems are the MS-DOS device names Windows refuses to let
// ANY file or directory be named — exactly, or with any extension ("con",
// "con.txt", and "con.tar.gz" are all refused alike; the check is against
// the name's STEM, everything before its first '.', case-insensitively).
var windowsReservedStems = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// escapeWindowsReservedStem percent-encodes seg's first byte when seg's stem
// (everything before its first '.', or all of seg when there is none)
// case-insensitively names an MS-DOS reserved device (finding 4, LOW): the
// earlier version of this function left every letter untouched, so a
// release tagged exactly "con" (or a monorepo component path leading with
// one) produced a literal "con" directory — one Windows refuses to create
// at all, silently taking every connector pinned to it down on that
// platform. Plugins do ship Windows binaries, and the daemon itself may run
// there one day, so this is escaped rather than merely documented as a gap.
//
// Escaping only the FIRST byte is enough to break the match (the stem is no
// longer one of the fixed reserved strings) while staying injective: the
// base percent-encoding scheme above always escapes a literal '%' as
// "%25", so no OTHER input can ever naturally produce a bare "%XX" escape
// of an otherwise-SAFE byte at this position the way this deliberately
// introduces here — seg[0] is always a plain, unescaped letter (C/P/A/N/L,
// upper or lower case) whenever this branch fires, so the result can never
// collide with anything the base scheme or truncateWithHash produces on
// their own.
func escapeWindowsReservedStem(seg string) string {
	if seg == "" {
		return seg
	}
	stem := seg
	if i := strings.IndexByte(seg, '.'); i >= 0 {
		stem = seg[:i]
	}
	if !windowsReservedStems[strings.ToLower(stem)] {
		return seg
	}
	return fmt.Sprintf("%%%02X", seg[0]) + seg[1:]
}

// isSafeVersionDirByte is RFC 3986's "unreserved" set: letters, digits, and
// "-_.~" — the characters percent-encoding never needs to touch, kept
// literal purely for short, readable directory names ("v1.0.0" stays
// "v1.0.0"). Every other byte — '/', '\\', '%', control characters, and
// anything else a filesystem might treat specially — is percent-encoded by
// the caller; a Windows-reserved device name among the safe bytes is caught
// separately by escapeWindowsReservedStem, since it is a property of the
// WHOLE segment, not any one byte.
func isSafeVersionDirByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.' || c == '~':
		return true
	default:
		return false
	}
}

// LoadInstallState reads the install state under dir. A missing, empty, or
// unreadable state file yields an EMPTY state with no error: install state is a
// cache of what is on disk, and a corrupt cache must degrade to "nothing is
// installed yet" rather than take the daemon down.
func LoadInstallState(dir string) *InstallState {
	s := &InstallState{Version: installStateVersion, dir: dir}
	if dir == "" {
		return s
	}
	b, err := os.ReadFile(filepath.Join(dir, installStateFile))
	if err != nil {
		return s
	}
	var on InstallState
	if err := yaml.Unmarshal(b, &on); err != nil {
		return s
	}
	switch on.Version {
	case installStateVersion:
		s.Plugins, s.Pending = on.Plugins, on.Pending
	case legacyStateVersion:
		s.Plugins = on.Plugins
		for i := range s.Plugins {
			s.Plugins[i].Legacy = s.Plugins[i].ManifestSchema < manifestSchema
		}
		s.restoreConsented()
	}
	s.markPreJail()
	return s
}

// jailEpoch is when the first conductor with the plugin jail was tagged: a
// binary on disk older than this was installed by a conductor without it.
var jailEpoch = time.Date(2026, 11, 6, 23, 33, 54, 0, time.UTC)

// markPreJail marks every build of a plugin installed before the plugin
// jail (Installed.PreJail). The evidence, any of: a record already marked;
// a record a conductor without the jail wrote that is not adopted yet
// (Legacy — a version 1 file, or a version 2 file whose adoption could not
// describe the build, which is what a host that cannot jail left behind);
// or a build whose binary predates the jail. Idempotent; the mark is saved
// with the state, so it outlives the old builds.
func (s *InstallState) markPreJail() {
	pre := map[string]bool{}
	for _, r := range s.Plugins {
		if r.PreJail || r.Legacy || binaryBefore(r.Path, jailEpoch) {
			pre[r.Key+"\x00"+r.Source] = true
		}
	}
	for i := range s.Plugins {
		s.Plugins[i].PreJail = s.Plugins[i].PreJail || pre[s.Plugins[i].Key+"\x00"+s.Plugins[i].Source]
	}
	for i := range s.Pending {
		s.Pending[i].PreJail = s.Pending[i].PreJail || pre[s.Pending[i].Key+"\x00"+s.Pending[i].Source]
	}
}

// preJail reports whether a plugin (key from source) was installed before
// the plugin jail: a new build of it carries the mark too.
func (s *InstallState) preJail(key, source string) bool {
	if s == nil {
		return false
	}
	for _, r := range s.Plugins {
		if r.Key == key && r.Source == source && r.PreJail {
			return true
		}
	}
	for _, p := range s.Pending {
		if p.Key == key && p.Source == source && p.PreJail {
			return true
		}
	}
	return false
}

func binaryBefore(path string, t time.Time) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.ModTime().Before(t)
}

// restoreConsented loads consentFile after a version 1 state file: the
// records adoption compares against, and the held updates the older
// conductor dropped, restored when their build is still on disk and is not
// what is installed now.
func (s *InstallState) restoreConsented() {
	b, err := os.ReadFile(filepath.Join(s.dir, consentFile))
	if err != nil {
		return
	}
	var c InstallState
	if yaml.Unmarshal(b, &c) != nil || c.Version != installStateVersion {
		return
	}
	s.consented = &c
	for _, p := range c.Pending {
		if _, installed := s.findSha(p.Key, p.Source, p.Sha256); installed || !binaryPresent(p.Path) {
			continue
		}
		p.Legacy = false
		s.Pending = append(s.Pending, p)
	}
}

// consentedFor is what the user last agreed to for key from source (before
// a rollback): the record of that exact build when there is one, else the
// newest one.
func (s *InstallState) consentedFor(key, source, sha string) (Installed, bool) {
	if s == nil || s.consented == nil {
		return Installed{}, false
	}
	if c, ok := s.consented.findSha(key, source, sha); ok && c.ManifestSchema >= manifestSchema {
		return c, true
	}
	all := versionsForSource(s.consented.AllVersions(key), source)
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].ManifestSchema >= manifestSchema {
			return all[i], true
		}
	}
	return Installed{}, false
}

// Dir returns the install directory this state was loaded from.
func (s *InstallState) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Side-by-side versions: install state holds up to one record per (Key,
// Resolved) pair now, not one record per Key — two connectors pinning
// different versions of the same plugin are two DISTINCT installed builds,
// each exec'd from its own path, never one silently standing in for the
// other (docs/wiki/Plugins.md "Side-by-side versions"). A LOCAL reference
// never reaches install state at all (SpecFromRef snapshots it independently
// by content hash every time it resolves — see snapshotLocal — so two
// connectors pointed at different local paths, or the same path at different
// content, already run side by side with no install-state change needed).
//
// On-disk FORMAT is unchanged: Installed already carried Resolved, so an
// old, single-version file — at most one record per Key — loads exactly as
// it always did; "migration" is a property of the new in-memory matching
// rules (Key+Resolved instead of Key alone), not a file rewrite. Get(key)
// alone (no version) keeps the pre-versioning meaning of "the one installed
// build for key" for a key that still has only one, and degrades to "an
// arbitrary representative" the moment a second version is added under it —
// every caller that must pick a SPECIFIC version among several uses
// GetVersion or GetForConstraint instead.

// Get returns a representative record for a "<kind-dir>/<name>" key: the
// highest Resolved version installed under it. For a key with only ever one
// version installed (the pre-versioning common case, and every runtime/engine
// key, which carry no per-instance multiplicity to split) this is simply
// "the" record, unchanged from before side-by-side versions existed. A
// caller that must resolve one specific configured instance's own version
// uses GetForConstraint instead.
func (s *InstallState) Get(key string) (Installed, bool) {
	all := s.AllVersions(key)
	if len(all) == 0 {
		return Installed{}, false
	}
	return all[len(all)-1], true
}

// GetVersion returns the record for key whose Resolved is EXACTLY version —
// an exact-pin or already-resolved lookup, no constraint matching. source
// disambiguates the increasingly real case (finding 2) of two DIFFERENT
// sources under one key having tagged the identical version text: when
// source is non-empty, only a record whose own Source matches it — or a
// legacy record with no recorded Source (written before this field existed,
// or by a caller that does not track it), which still matches ANY source
// since it cannot possibly be a different source's record — is returned.
// A record belonging to a DIFFERENT non-empty source is never returned as a
// fallback: finding 1 (HIGH) was exactly this — a query for one source's
// (key, tag) silently handing back another source's record (and its
// manifest) when nothing from the queried source existed yet, so a freshly
// added instance inherited a stranger's permission manifest. When no
// qualifying record exists, GetVersion reports not found rather than
// substituting one. source == "" keeps the old, unqualified "first match"
// behavior for a caller that does not know or care which source wrote it.
func (s *InstallState) GetVersion(key, version, source string) (Installed, bool) {
	if s == nil {
		return Installed{}, false
	}
	for _, p := range s.Plugins {
		if p.Key != key || p.Resolved != version {
			continue
		}
		if source == "" || p.Source == "" || p.Source == source {
			return p, true
		}
	}
	return Installed{}, false
}

// versionsForSource filters all to the records matching source: a record
// whose own Source is empty (pre-dates this field, or was written by a
// caller that does not track it) still matches — it cannot be a DIFFERENT
// source's record if nothing ever recorded one. source == "" matches
// everything (a caller, like the old single-source world, that does not
// distinguish).
func versionsForSource(all []Installed, source string) []Installed {
	if source == "" {
		return all
	}
	out := make([]Installed, 0, len(all))
	for _, in := range all {
		if in.Source == "" || in.Source == source {
			out = append(out, in)
		}
	}
	return out
}

// GetForConstraint resolves ONE instance's own `use:` reference against every
// version of key currently installed FROM ITS OWN SOURCE, returning the best
// (highest) match — the install-state half of the version split a Manager
// builds its process groups from (see groupInstances). An exact pin matches
// only that literal Resolved tag (via BestMatch's semver compare — see
// config.SatisfiesConstraint); an unpinned/ranged reference tracks the
// highest installed version the range accepts, exactly like a fresh resolve
// would, but OFFLINE, against whatever Reconcile has already fetched.
//
// Finding 2 (HIGH): filtering by u.Source() matters the moment two DIFFERENT
// sources under one key have both tagged the same (or an overlapping) range
// of version text — without it, BestMatch could return a tag that exists
// only under the OTHER source, and the lookup below would hand this
// instance a record (sha, path) that was never fetched from ITS source at
// all.
func (s *InstallState) GetForConstraint(key string, u config.Use) (Installed, bool) {
	all := versionsForSource(s.AllVersions(key), u.Source())
	if len(all) == 0 {
		return Installed{}, false
	}
	if u.Version == "" && len(all) == 1 {
		// Genuinely UNCONSTRAINED (no @version at all) with nothing to
		// disambiguate — the sole installed record for key IS this
		// instance's build regardless of whether its Resolved happens to
		// parse as semver (a record a test, or some other non-fetch path,
		// wrote with no real release tag). A REAL constraint (a pin or a
		// range) always goes through BestMatch below instead, even with
		// only one candidate — "the one installed build" is not
		// automatically "the build this constraint accepts".
		return all[0], true
	}
	tags := make([]string, len(all))
	for i, in := range all {
		tags[i] = in.Resolved
	}
	tag, ok := config.BestMatch(tags, u.TagPrefix(), u.Version)
	if !ok {
		return Installed{}, false
	}
	for _, in := range all {
		if in.Resolved == tag {
			return in, true
		}
	}
	return Installed{}, false
}

// AllVersions returns every installed record for key, sorted by Resolved
// ascending using the SAME semver comparison config.BestMatch resolves
// constraints with (config.CompareVersions) — never a plain lexical byte
// compare, which puts "v1.10.0" BEFORE "v1.9.0" (lexically "1.1" < "1.9")
// and silently handed Get() the wrong "highest" record the moment a
// plugin's installed versions crossed a double-digit component. Empty when
// nothing is installed under key.
func (s *InstallState) AllVersions(key string) []Installed {
	if s == nil {
		return nil
	}
	var out []Installed
	for _, p := range s.Plugins {
		if p.Key == key {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return config.CompareVersions(out[i].Resolved, out[j].Resolved) < 0 })
	return out
}

// Keys lists the DISTINCT installed keys, sorted — one entry per name even
// when several versions are installed under it.
func (s *InstallState) Keys() []string {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(s.Plugins))
	for _, p := range s.Plugins {
		if !seen[p.Key] {
			seen[p.Key] = true
			out = append(out, p.Key)
		}
	}
	sort.Strings(out)
	return out
}

// Put inserts or replaces the record for in's (Key, Resolved, Source)
// triple — together the identity now, so installing a second version under
// an already-installed key ADDS a record rather than overwriting the
// existing one.
//
// Finding 2 (HIGH): Source joined the identity alongside (Key, Resolved)
// because nothing in this package stops two DIFFERENT sources under one key
// from resolving to the identical tag TEXT — config.validatePluginRefs
// refuses that within a single config, but not across a sequential config
// change, and install state persists across those. Matching on (Key,
// Resolved) alone would let the SECOND source's Put silently overwrite the
// first source's record — same map slot, different sha/path — discarding
// it rather than recording both, even though reconcileInstances' bucket
// dedup (resolve.go) now fetches each source's own build independently.
func (s *InstallState) Put(in Installed) {
	if s == nil {
		return
	}
	in.PreJail = in.PreJail || s.preJail(in.Key, in.Source)
	for i, p := range s.Plugins {
		if p.Key == in.Key && p.Resolved == in.Resolved && p.Source == in.Source {
			s.Plugins[i] = in
			return
		}
	}
	s.Plugins = append(s.Plugins, in)
}

// Delete removes every record for key (every installed version), reporting
// whether anything was present. Used by `plugin remove` and by a prune that
// has decided NOTHING about key is referenced any more.
func (s *InstallState) Delete(key string) bool {
	if s == nil {
		return false
	}
	found := false
	kept := s.Plugins[:0]
	for _, p := range s.Plugins {
		if p.Key == key {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	s.Plugins = kept
	return found
}

// DeleteVersion removes exactly one (key, resolved) record — the GC
// primitive: a version no live config reference resolves to any more is
// dropped while a SIBLING version of the same key that is still referenced
// stays installed and running.
func (s *InstallState) DeleteVersion(key, resolved string) bool {
	if s == nil {
		return false
	}
	for i, p := range s.Plugins {
		if p.Key == key && p.Resolved == resolved {
			s.Plugins = append(s.Plugins[:i], s.Plugins[i+1:]...)
			return true
		}
	}
	return false
}

// deleteRecord removes the record of key at resolved from source.
func (s *InstallState) deleteRecord(key, resolved, source string) {
	if s == nil {
		return
	}
	for i, p := range s.Plugins {
		if p.Key == key && p.Resolved == resolved && p.Source == source {
			s.Plugins = append(s.Plugins[:i], s.Plugins[i+1:]...)
			return
		}
	}
}

// GCVersions drops every installed (key, version) pair NOT in keep, removing
// its on-disk directory too — the GC half of side-by-side versions: a
// version no currently-configured instance resolves to any more (the config
// changed, or every instance that pinned it moved on) is uninstalled, while
// a SIBLING version of the same key that IS still referenced is left exactly
// as it is, running. keep is built by the caller from the live, reconciled
// set (ReconcileVersions' results, or an equivalent walk of the current
// config's resolved groups) — GCVersions itself has no notion of what
// "still referenced" means, same division of responsibility as the older
// whole-key Reconcile prune.
func (s *InstallState) GCVersions(keep map[VersionKey]bool) ([]VersionKey, error) {
	if s == nil {
		return nil, nil
	}
	var (
		dropped []VersionKey
		kept    []Installed
	)
	for _, p := range s.Plugins {
		vk := VersionKey{Key: p.Key, Resolved: p.Resolved, Source: p.Source}
		if keep[vk] {
			kept = append(kept, p)
			continue
		}
		dropped = append(dropped, vk)
	}
	if len(dropped) == 0 {
		return nil, nil
	}
	s.Plugins = kept
	for _, vk := range dropped {
		dir := BinDirForVersion(s.dir, vk.Key, vk.Resolved, vk.Source)
		keyDir := BinDirFor(s.dir, vk.Key)
		if err := removeVersionDir(keyDir, dir); err != nil {
			return dropped, fmt.Errorf("plugin %s@%s: %w", vk.Key, vk.Resolved, err)
		}
	}
	return dropped, nil
}

// removeVersionDir is GCVersions' actual removal step for ONE installed
// version's directory, pulled out as its own function so the guard below is
// independently testable (finding 5, test gap): sanitizeVersionDir is now
// injective and refuses "." / ".." (finding 1), so a REAL GCVersions pass
// can no longer hand this a dir that escapes keyDir — which means a test
// that only ever drives this through GCVersions itself can delete the
// isStrictlyWithin check entirely and still pass every test, the exact gap
// the reviewer flagged. Testing removeVersionDir directly, with a
// hand-built (keyDir, dir) pair that escapes, proves the guard still does
// real work as defense in depth — independent of whichever upstream
// sanitization currently (and may not always) prevent reaching it.
//
// Finding 5 (security, defense in depth): a corrupt or tampered Key, or a
// future change to sanitizeVersionDir or BinDirForVersion, must not be able
// to turn this into "remove something outside this one version's own
// directory" ever again. dir must be strictly INSIDE keyDir (the plugin's
// own install directory, BinDirFor) — a claim no sanitization bug anywhere
// upstream can quietly violate.
func removeVersionDir(keyDir, dir string) error {
	if !isStrictlyWithin(keyDir, dir) {
		return fmt.Errorf("refusing to remove %s — it is not strictly inside %s (corrupt or tampered install state?)", dir, keyDir)
	}
	return os.RemoveAll(dir)
}

// isStrictlyWithin reports whether child is a (possibly multi-level) child
// of parent — never parent itself, and never an escape above it (a ".."
// component that resolves outside parent). GCVersions' last line of defense
// before RemoveAll: even if sanitizeVersionDir or a Key ever let something
// slip through, this refuses to touch anything outside the specific
// plugin's own install directory.
func isStrictlyWithin(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// VersionKey identifies one installed (plugin key, resolved version, source)
// triple — GCVersions' keep-set element and ReconcileVersions' result key.
//
// Finding 2 (HIGH): Source joined Key+Resolved for the same reason it joined
// Put's identity (see Put's doc comment) — two different sources under one
// key can both tag the identical version text, and the GC keep-set exists
// precisely to tell "still referenced" apart from "no longer referenced"
// per INSTALLED RECORD; collapsing two different sources' records onto one
// VersionKey would let keeping one silently keep (or dropping one silently
// drop) a record that actually belongs to an unrelated source.
type VersionKey struct {
	Key      string
	Resolved string
	Source   string
}

// Save writes the state back, sorted by key so the file is stable across
// runs. The write is ATOMIC (temp file in the same directory, fsynced, then
// renamed over the final path): a concurrent reader — another `conductor`
// invocation's LoadInstallState, or this daemon's own install dir on a crash
// mid-write — must never observe a truncated or half-written file. A plain
// os.WriteFile truncates the existing file in place first, so a reader (or a
// crash) landing between the truncate and the write sees an empty/corrupt
// file; rename is atomic on the same filesystem and always resolves to
// either the old, complete content or the new, complete content.
func (s *InstallState) Save() error {
	if s == nil || s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("plugin install dir: %w", err)
	}
	sort.Slice(s.Plugins, func(i, j int) bool { return s.Plugins[i].Key < s.Plugins[j].Key })
	s.Version = installStateVersion
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	header := "# conductor plugin install state — LOCAL to this machine, written by\n" +
		"# `conductor init` / `conductor plugin update`. Do not commit it.\n"
	content := append([]byte(header), b...)

	// The consent copy first: a crash between the two leaves the state
	// file as it was, never a newer state file with an older copy.
	if err := writeAtomic(s.dir, consentFile, content); err != nil {
		return err
	}
	return writeAtomic(s.dir, installStateFile, content)
}

// writeAtomic writes content to dir/name through a synced temp file renamed
// over it.
func writeAtomic(dir, name string, content []byte) error {
	final := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("plugin install state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below has consumed it
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("plugin install state: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("plugin install state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("plugin install state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("plugin install state: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("plugin install state: %w", err)
	}
	// Best-effort: fsync the directory entry too, so the rename itself
	// survives a crash (POSIX does not guarantee a rename is durable until
	// the containing directory is synced).
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
