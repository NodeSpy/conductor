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
}

// IsZero reports whether the plugin declared no capabilities at all.
func (m Manifest) IsZero() bool {
	return len(m.Egress) == 0 && len(m.Commands) == 0 && len(m.FS) == 0 && !m.Spawns && len(m.Env) == 0
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

// BinDirFor is where a plugin's binary is installed: <install-dir>/<key>.
func BinDirFor(dir, key string) string { return filepath.Join(dir, filepath.FromSlash(key)) }

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

// Get returns the record for a "<kind-dir>/<name>" key.
func (s *InstallState) Get(key string) (Installed, bool) {
	if s == nil {
		return Installed{}, false
	}
	for _, p := range s.Plugins {
		if p.Key == key {
			return p, true
		}
	}
	return Installed{}, false
}

// Keys lists the installed keys, sorted.
func (s *InstallState) Keys() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Plugins))
	for _, p := range s.Plugins {
		out = append(out, p.Key)
	}
	sort.Strings(out)
	return out
}

// Put inserts or replaces a record.
func (s *InstallState) Put(in Installed) {
	if s == nil {
		return
	}
	for i, p := range s.Plugins {
		if p.Key == in.Key {
			s.Plugins[i] = in
			return
		}
	}
	s.Plugins = append(s.Plugins, in)
}

// Delete removes a record, reporting whether it was present.
func (s *InstallState) Delete(key string) bool {
	if s == nil {
		return false
	}
	for i, p := range s.Plugins {
		if p.Key == key {
			s.Plugins = append(s.Plugins[:i], s.Plugins[i+1:]...)
			return true
		}
	}
	return false
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
