package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
const installStateVersion = 1

// Manifest is the permission manifest a plugin declared at install time — what
// it says it needs, recorded so it is known BEFORE the plugin runs on any
// later boot, and so a change on update is visible rather than silent.
type Manifest struct {
	// Egress are the "host:port" targets the plugin calls.
	Egress []string `yaml:"egress,omitempty"`
	// Commands are the commands it spawns.
	Commands []string `yaml:"commands,omitempty"`
	// FS are the filesystem paths it needs.
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
	return len(m.Egress) == 0 && len(m.Commands) == 0 && len(m.FS) == 0 && !m.Spawns && len(m.Env) == 0 && !m.SingleProcess
}

// Summary renders the manifest as one line for logs and install review.
func (m Manifest) Summary() string {
	if m.IsZero() {
		return "no declared capabilities"
	}
	var parts []string
	if len(m.Egress) > 0 {
		parts = append(parts, "network "+strings.Join(m.Egress, ","))
	}
	if len(m.Commands) > 0 {
		parts = append(parts, "commands "+strings.Join(m.Commands, ","))
	} else if m.Spawns {
		parts = append(parts, "commands (unnamed)")
	}
	if len(m.FS) > 0 {
		parts = append(parts, "fs "+strings.Join(m.FS, ","))
	}
	if len(m.Env) > 0 {
		parts = append(parts, "env "+strings.Join(m.Env, ","))
	}
	if m.SingleProcess {
		parts = append(parts, "single_process (every instance shares one process)")
	}
	return strings.Join(parts, "; ")
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

	dir string `yaml:"-"`
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

// BinDirForVersion is where ONE resolved version of a plugin is installed:
// <install-dir>/<key>/<version>. Side by side versions need their own
// directory each — two versions of a plugin publish the SAME per-platform
// asset filename (RemoteSource.AssetName), so fetching a second version into
// the bare key directory would silently overwrite the first version's binary
// on disk out from under any instance still pinned to it. resolved is
// sanitized to a single path segment (no separators) so a monorepo's
// component-prefixed tag ("connectors/widget/v1.2.3") can't escape the key's
// own directory.
func BinDirForVersion(dir, key, resolved string) string {
	return filepath.Join(BinDirFor(dir, key), sanitizeVersionDir(resolved))
}

// sanitizeVersionDir maps a resolved version/tag to a safe single path
// segment.
//
// Finding 5 (security): mapping "/" and "\\" is not enough on its own — a
// value that IS (or, after that mapping, cleans to) "." or ".." is still a
// single "segment" with no separator in it, so the replacer above leaves it
// untouched, and BinDirForVersion would then resolve to the plugin's OWN
// key directory (".") or its PARENT (".."). A tampered installed.yaml
// record with Resolved: ".." makes GCVersions RemoveAll the ENTIRE
// connectors/ or runtimes/ directory, every version of every plugin, since
// BinDirForVersion(key, "..") IS that parent. A real release tag is never
// exactly "." or "..", so reaching this is already evidence of a corrupt or
// tampered record; map it to a safe, unambiguous literal rather than ever
// letting a path operation resolve outside this one version's own
// directory. Mirrors the same "reject the special entries, not just
// separators" rule Client.StagingDir applies to a leading dot.
func sanitizeVersionDir(v string) string {
	v = strings.NewReplacer("/", "_", "\\", "_").Replace(v)
	// filepath.Clean on a separator-free string only ever produces "." or
	// ".." from an input that already WAS "", ".", or ".." — there is no
	// other way a slash-free segment cleans to either.
	if cleaned := filepath.Clean(v); cleaned == "." || cleaned == ".." {
		return "_" + cleaned + "_"
	}
	return v
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
	if on.Version != installStateVersion {
		return s
	}
	s.Plugins = on.Plugins
	return s
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
// an exact-pin or already-resolved lookup, no constraint matching.
func (s *InstallState) GetVersion(key, version string) (Installed, bool) {
	if s == nil {
		return Installed{}, false
	}
	for _, p := range s.Plugins {
		if p.Key == key && p.Resolved == version {
			return p, true
		}
	}
	return Installed{}, false
}

// GetForConstraint resolves ONE instance's own `use:` reference against every
// version of key currently installed, returning the best (highest) match —
// the install-state half of the version split a Manager builds its process
// groups from (see groupInstances). An exact pin matches only that literal
// Resolved tag (via BestMatch's semver compare — see config.SatisfiesConstraint);
// an unpinned/ranged reference tracks the highest installed version the range
// accepts, exactly like a fresh resolve would, but OFFLINE, against whatever
// Reconcile has already fetched.
func (s *InstallState) GetForConstraint(key string, u config.Use) (Installed, bool) {
	all := s.AllVersions(key)
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

// Put inserts or replaces the record for in's (Key, Resolved) pair — the two
// together are the identity now, so installing a second version under an
// already-installed key ADDS a record rather than overwriting the existing
// one.
func (s *InstallState) Put(in Installed) {
	if s == nil {
		return
	}
	for i, p := range s.Plugins {
		if p.Key == in.Key && p.Resolved == in.Resolved {
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
		vk := VersionKey{Key: p.Key, Resolved: p.Resolved}
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
		dir := BinDirForVersion(s.dir, vk.Key, vk.Resolved)
		// Finding 5 (security, defense in depth): sanitizeVersionDir already
		// refuses a Resolved that is (or cleans to) "." or "..", but this is
		// the LAST line of defense before an actual RemoveAll — a corrupt or
		// tampered Key, or a future change to either helper, must not be
		// able to turn this into "remove something outside this one
		// version's own directory" ever again. keyDir is the plugin's own
		// install directory (BinDirFor); dir must be strictly INSIDE it, a
		// claim no sanitization bug anywhere upstream can quietly violate.
		keyDir := BinDirFor(s.dir, vk.Key)
		if !isStrictlyWithin(keyDir, dir) {
			return dropped, fmt.Errorf("plugin %s@%s: refusing to remove %s — it is not strictly inside %s (corrupt or tampered install state?)", vk.Key, vk.Resolved, dir, keyDir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return dropped, fmt.Errorf("plugin %s@%s: remove install dir: %w", vk.Key, vk.Resolved, err)
		}
	}
	return dropped, nil
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

// VersionKey identifies one installed (plugin key, resolved version) pair —
// GCVersions' keep-set element and ReconcileVersions' result key.
type VersionKey struct {
	Key      string
	Resolved string
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

	final := filepath.Join(s.dir, installStateFile)
	tmp, err := os.CreateTemp(s.dir, "."+installStateFile+".tmp-*")
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
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
