package plugin

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
)

// Manager owns the set of external plugin Clients for one loaded config and
// their lifetime. Bundled connectors/runtimes are NOT here — the manager only
// holds the plugins derived from non-builtin `use:` references
// (config.PluginRefs), keyed by "<kind-dir>/<name>".
type Manager struct {
	clients map[string]*Client // immutable after NewManager (pointers; a client's process is swapped in place by Reload)
	order   []string           // immutable after NewManager

	// mu guards specs + decls — the maps mutated after construction (Reload
	// updates a plugin's binary-identity fields; StartAndDescribe records its
	// self-description). clients/order are built once and read-only thereafter.
	mu sync.RWMutex
	// specs are the resolved specs, keyed by "<kind>/<name>".
	specs map[string]Spec
	// decls records each plugin's Decl from its FIRST StartAndDescribe — the
	// surface the daemon's registrations were built against, captured at boot
	// BEFORE any auto-update overwrites the binary in place. hot-reload compares
	// a new build's Decl against this to decide swap-in-place vs restart
	// (SameReloadSurface); re-describing the running client is unsafe because the
	// binary at its (stable) path may already be the new build.
	decls map[string]*Decl
}

// SpecFromRef resolves one derived config.PluginRef into a runnable Spec.
//
//   - A LOCAL reference (`use: ./bin/conductor-jira`) points straight at the
//     operator's own binary, made absolute against configDir. There is no sha to
//     pin: a development binary changes on every build, so the guarantee here is
//     the safe-permissions check (an attacker-swappable path is still refused),
//     not a pin.
//   - A REMOTE reference is served from local install state, which carries the
//     verified sha recorded when it was fetched. With no install state the
//     BinPath is empty and Start reports "not installed — run conductor init"
//     rather than trying to exec a URL.
//
// It performs no I/O on the binary (verification happens at Start).
func SpecFromRef(ref config.PluginRef, configDir string, inst Installed, ok bool) Spec {
	s := Spec{
		Name:               ref.Name,
		Kind:               Kind(ref.Kind()),
		Provides:           ref.Name,
		Version:            ref.Version(),
		Isolation:          ref.Isolation,
		IsolationDefaulted: ref.IsolationDefaulted,
		TrustFull:          ref.TrustFull,
		Network:            ref.Network,
		AllowSecrets:       ref.AllowSecrets,
		Use:                ref.Use,
	}
	if ref.Use.Origin == config.OriginLocal {
		bin := ref.Use.Path
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(configDir, bin)
		}
		s.BinPath, s.Local = bin, true
		return s
	}
	if ok {
		s.BinPath, s.Sha256, s.Resolved = inst.Path, inst.Sha256, inst.Resolved
		s.Manifest = inst.Manifest
	}
	return s
}

// NewManager builds a Manager from the config's derived plugin set. configDir is
// the directory the config file lives in (for resolving local paths); state
// supplies each remote plugin's installed binary. deps is shared by every
// client. It does not start any subprocess.
func NewManager(plugins map[string]config.PluginRef, configDir string, state *InstallState, deps Deps) *Manager {
	m := &Manager{
		clients: make(map[string]*Client, len(plugins)),
		specs:   make(map[string]Spec, len(plugins)),
		decls:   make(map[string]*Decl, len(plugins)),
	}
	for key := range plugins {
		m.order = append(m.order, key)
	}
	sort.Strings(m.order)
	for _, key := range m.order {
		ref := plugins[key]
		inst, ok := state.Get(key)
		spec := SpecFromRef(ref, configDir, inst, ok)
		m.specs[key] = spec
		m.clients[key] = NewClient(spec, deps)
	}
	return m
}

// Names returns the plugin names in sorted order.
func (m *Manager) Names() []string { return append([]string(nil), m.order...) }

// Spec returns a plugin's resolved spec.
func (m *Manager) Spec(name string) (Spec, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.specs[name]
	return s, ok
}

// Client returns a plugin's client.
func (m *Manager) Client(name string) (*Client, bool) {
	c, ok := m.clients[name]
	return c, ok
}

// ConnectorSpecs / RuntimeSpecs / EngineSpecs list the plugins of each kind.
func (m *Manager) ConnectorSpecs() []Spec { return m.specsOfKind(KindConnector) }
func (m *Manager) RuntimeSpecs() []Spec   { return m.specsOfKind(KindRuntime) }

// EngineSpecs lists the step-engine plugins — the ones a code step's `use:`
// resolved to.
func (m *Manager) EngineSpecs() []Spec { return m.specsOfKind(KindStep) }

func (m *Manager) specsOfKind(k Kind) []Spec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Spec
	for _, name := range m.order {
		if m.specs[name].Kind == k {
			out = append(out, m.specs[name])
		}
	}
	return out
}

// Reload swaps one plugin's subprocess to newSpec's binary IN PLACE (see
// Client.Reload) and updates its recorded binary-identity fields. The same
// *Client pointer stays in clients, so every consumer keeps driving the new
// build with no re-registration. client.Reload can block draining in-flight
// calls, so it runs OUTSIDE m.mu. Returns Client.Reload's error
// (ErrReloadUnsupported/ErrReloadBusy) for the caller to map to a restart.
func (m *Manager) Reload(key string, newSpec Spec) error {
	c, ok := m.clients[key] // clients is immutable post-construction; no lock
	if !ok {
		return fmt.Errorf("plugin %q not found", key)
	}
	if err := c.Reload(newSpec); err != nil {
		return err
	}
	m.mu.Lock()
	if s, ok := m.specs[key]; ok {
		s.BinPath, s.Sha256, s.Resolved = newSpec.BinPath, newSpec.Sha256, newSpec.Resolved
		m.specs[key] = s
	}
	m.mu.Unlock()
	return nil
}

// Close stops every plugin subprocess.
func (m *Manager) Close() error {
	for _, c := range m.clients {
		_ = c.Close()
	}
	return nil
}

// StartAndDescribe starts a plugin and returns its self-description — the
// connector/runtime registration path. On any failure the client is left
// stopped and the error is returned (the caller disables that plugin's type,
// mirroring the connector convention of disable-not-crash).
func (m *Manager) StartAndDescribe(ctx context.Context, name string) (*Decl, error) {
	c, ok := m.clients[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	decl, err := c.Describe(ctx)
	if err != nil {
		return nil, err
	}
	// Record the boot surface once — the first successful describe — so hot-
	// reload can compare a new build against what the registrations were built
	// from. Don't overwrite a prior record (StartAndDescribe is idempotent).
	m.mu.Lock()
	if _, seen := m.decls[name]; !seen {
		m.decls[name] = decl
	}
	m.mu.Unlock()
	return decl, nil
}

// Decl returns the self-description recorded at a plugin's first
// StartAndDescribe (the surface its registrations were built against), or nil.
func (m *Manager) Decl(name string) (*Decl, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.decls[name]
	return d, ok
}
