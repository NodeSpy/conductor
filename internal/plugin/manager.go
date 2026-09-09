package plugin

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// Manager owns the set of external plugin Clients for one loaded config and
// their lifetime. Bundled connectors/runtimes are NOT here — the manager only
// holds the `plugins:` entries.
type Manager struct {
	clients map[string]*Client
	specs   map[string]Spec
	order   []string
}

// SpecFromRef resolves one config.PluginRef into a runnable Spec, making the
// source path absolute relative to configDir. It performs no I/O on the binary
// (verification happens at Start).
func SpecFromRef(name string, ref config.PluginRef, configDir string) Spec {
	bin := ref.Source
	if !filepath.IsAbs(bin) {
		bin = filepath.Join(configDir, bin)
	}
	return Spec{
		Name:            name,
		Kind:            Kind(ref.Kind),
		Provides:        ref.ProvidesName(name),
		Version:         ref.Version,
		BinPath:         bin,
		Args:            ref.Args,
		Sha256:          ref.Sha256,
		AllowUnverified: ref.AllowUnverified,
		Isolation:       ref.Isolation,
		AllowSecrets:    ref.AllowSecrets,
	}
}

// NewManager builds a Manager from the config's plugins: block. configDir is
// the directory the config file lives in (for resolving relative sources).
// deps is shared by every client. It does not start any subprocess.
func NewManager(plugins map[string]config.PluginRef, configDir string, deps Deps) *Manager {
	m := &Manager{
		clients: make(map[string]*Client, len(plugins)),
		specs:   make(map[string]Spec, len(plugins)),
	}
	for name := range plugins {
		m.order = append(m.order, name)
	}
	sort.Strings(m.order)
	for _, name := range m.order {
		spec := SpecFromRef(name, plugins[name], configDir)
		m.specs[name] = spec
		m.clients[name] = NewClient(spec, deps)
	}
	return m
}

// Names returns the plugin names in sorted order.
func (m *Manager) Names() []string { return append([]string(nil), m.order...) }

// Spec returns a plugin's resolved spec.
func (m *Manager) Spec(name string) (Spec, bool) {
	s, ok := m.specs[name]
	return s, ok
}

// Client returns a plugin's client.
func (m *Manager) Client(name string) (*Client, bool) {
	c, ok := m.clients[name]
	return c, ok
}

// ConnectorSpecs / RuntimeSpecs list the plugins of each kind.
func (m *Manager) ConnectorSpecs() []Spec { return m.specsOfKind(KindConnector) }
func (m *Manager) RuntimeSpecs() []Spec   { return m.specsOfKind(KindRuntime) }

func (m *Manager) specsOfKind(k Kind) []Spec {
	var out []Spec
	for _, name := range m.order {
		if m.specs[name].Kind == k {
			out = append(out, m.specs[name])
		}
	}
	return out
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
	return c.Describe(ctx)
}
