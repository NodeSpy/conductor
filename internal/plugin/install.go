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
//	  connectors/sentry/conductor-sentry_linux_amd64
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
}

// IsZero reports whether the plugin declared no capabilities at all.
func (m Manifest) IsZero() bool {
	return len(m.Egress) == 0 && len(m.Commands) == 0 && len(m.FS) == 0 && !m.Spawns
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
	return strings.Join(parts, "; ")
}

// Installed is one installed plugin's local record.
type Installed struct {
	// Key is "<kind-dir>/<name>" — "connectors/sentry".
	Key string `yaml:"key"`
	// Kind is connector | runtime.
	Kind string `yaml:"kind"`
	// Name is the implementation name (connector type / runtime name).
	Name string `yaml:"name"`
	// Use is the reference as written in the config, so a changed reference is
	// detectable without re-resolving.
	Use string `yaml:"use"`
	// Source is the canonical fetch source ("github.com/o/r//comp").
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

// Save writes the state back, sorted by key so the file is stable across runs.
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
	return os.WriteFile(filepath.Join(s.dir, installStateFile), append([]byte(header), b...), 0o600)
}
