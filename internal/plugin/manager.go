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
//
// A key holds AT MOST ONE of two shapes of client, decided once at
// construction by the Spec's Kind and SharedProcess flag:
//
//   - a single, persistent, eagerly-created client in `clients` — every
//     runtime and engine key (neither has more than one "instance" sharing a
//     key), and a connector key whose operator opted into shared_process:;
//   - a lazily-created client PER CONFIGURED CONNECTOR INSTANCE, in
//     `instClients[key][instance]` (Manager.InstanceClient) — the default for
//     a connector key: multi-instance isolation (docs/wiki/Plugins.md) gives
//     each configured instance of an external plugin its own subprocess, its
//     own sandbox/env/staging dir, rather than sharing the type's one
//     process the way every plugin kind did before this existed.
//
// The connector TYPE-level describe/install probe (ProbeDescribe) uses
// neither of these: it is a throwaway client, started, described, and closed
// without ever being retained here — the type-level Decl is a property of the
// installed BINARY, not of any one configured instance, so there is nothing
// to keep running once it is known.
type Manager struct {
	clients map[string]*Client // immutable after NewManager (pointers; a client's process is swapped in place by Reload)
	order   []string           // immutable after NewManager
	deps    Deps               // shared by every client this Manager ever creates, including lazily

	// mu guards specs + decls — the maps mutated after construction (Reload
	// updates a plugin's binary-identity fields; StartAndDescribe/ProbeDescribe
	// record a self-description). clients/order are built once and read-only
	// thereafter.
	mu sync.RWMutex
	// specs are the resolved specs, keyed by "<kind>/<name>".
	specs map[string]Spec
	// decls records each plugin's Decl from its FIRST StartAndDescribe/
	// ProbeDescribe — the surface the daemon's registrations were built
	// against, captured at boot BEFORE any auto-update overwrites the binary
	// in place. hot-reload compares a new build's Decl against this to decide
	// swap-in-place vs restart (SameReloadSurface); re-describing a running
	// client is unsafe because the binary at its (stable) path may already be
	// the new build.
	decls map[string]*Decl

	// instMu guards instClients + closed — see the per-instance shape above.
	// Separate from mu: InstanceClient must not block a concurrent Spec/Decl
	// read (or vice versa) just because it is lazily creating a client.
	instMu sync.Mutex
	// instClients holds each connector key's per-instance clients, created on
	// first InstanceClient call for that (key, instance) pair. nil entries are
	// never stored; an absent map for a key simply means no instance of it has
	// been asked for yet.
	instClients map[string]map[string]*Client
	// closed is set by Close so an InstanceClient call racing with shutdown
	// creates no client that Close would never reach.
	closed bool
}

// SpecFromRef resolves one derived config.PluginRef into a runnable Spec.
//
//   - A LOCAL reference (`use: ./bin/conductor-widget`) points at the
//     operator's own binary, made absolute against configDir, then SNAPSHOTTED
//     by content hash (snapshotLocal) into a private, content-addressed copy
//     under the state dir — the local-build TOCTOU fix: a development binary
//     changes on every rebuild, so every verify()+exec of the raw path could
//     otherwise see different bytes across the type probe, a per-instance
//     probe, and the live spawn. The returned Spec's BinPath/Sha256 name the
//     SNAPSHOT, which verify() then pins exactly like a release sha — not the
//     mutable source path. A rebuild is picked up only the next time this
//     function resolves this reference again (a reload or restart), never
//     mid-life. A snapshot failure (no writable state dir, an unreadable
//     source) is recorded on the Spec's SnapshotErr rather than returned, so a
//     caller that only wants to LIST plugins (never execs them) still gets a
//     Spec back; Start/verify surfaces the failure the moment something tries
//     to run it.
//   - A REMOTE reference is served from local install state, which carries the
//     verified sha recorded when it was fetched. With no install state the
//     BinPath is empty and Start reports "not installed — run conductor init"
//     rather than trying to exec a URL.
//
// Besides the local-build snapshot (a private copy, never executed by this
// call), it performs no I/O on the binary — verification happens at Start.
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
		AllowEnv:           ref.AllowEnv,
		SharedProcess:      ref.SharedProcess,
		Use:                ref.Use,
	}
	if ref.Use.Origin == config.OriginLocal {
		bin := ref.Use.Path
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(configDir, bin)
		}
		s.Local = true
		if snap, sha, err := snapshotLocal(ref.Name, bin); err != nil {
			// Record, don't fail here: SpecFromRef has no error return, and a
			// display-only caller (`plugin list`) must still get a Spec back.
			// BinPath stays the raw source path so pluginStatus/VerifyOnly can
			// at least say SOMETHING is there; Start (the only caller that
			// would ever run it) hits SnapshotErr first and refuses clearly.
			s.BinPath = bin
			s.SnapshotErr = err
		} else {
			s.BinPath, s.Sha256 = snap, sha
		}
		return s
	}
	if ok {
		s.BinPath, s.Sha256, s.Resolved = inst.Path, inst.Sha256, inst.Resolved
		s.Manifest = inst.Manifest
		s.ReleaseVerified = inst.ReleaseVerified && inst.Sha256 != ""
	}
	return s
}

// NewManager builds a Manager from the config's derived plugin set. configDir is
// the directory the config file lives in (for resolving local paths); state
// supplies each remote plugin's installed binary. deps is shared by every
// client, including one created later by InstanceClient. It does not start any
// subprocess.
//
// A runtime or engine key, and a connector key with shared_process: true, get
// their persistent client HERE, eagerly — unchanged from before multi-instance
// isolation. A plain connector key gets none yet: InstanceClient creates its
// per-instance clients lazily, on the builder's first call for each configured
// instance.
func NewManager(plugins map[string]config.PluginRef, configDir string, state *InstallState, deps Deps) *Manager {
	m := &Manager{
		clients:     make(map[string]*Client, len(plugins)),
		specs:       make(map[string]Spec, len(plugins)),
		decls:       make(map[string]*Decl, len(plugins)),
		instClients: make(map[string]map[string]*Client, len(plugins)),
		deps:        deps,
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
		if spec.Kind != KindConnector || spec.SharedProcess {
			m.clients[key] = NewClient(spec, deps)
		}
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

// Client returns a plugin's PERSISTENT client — the one every runtime and
// engine key has, and the one a shared_process: true connector key has. A
// plain connector key (the default, per-instance-isolated shape) has none:
// use InstanceClient for those.
func (m *Manager) Client(name string) (*Client, bool) {
	c, ok := m.clients[name]
	return c, ok
}

// HasLiveClient reports whether key has at least one live *Client — its
// persistent one (Client), or any per-instance one InstanceClient has
// created. Where code used to check Client(key) alone to mean "is this plugin
// live", that is no longer sufficient for a (per-instance-isolated) connector
// key, which never gets a persistent client at all.
func (m *Manager) HasLiveClient(key string) bool {
	if _, ok := m.clients[key]; ok {
		return true
	}
	m.instMu.Lock()
	defer m.instMu.Unlock()
	return len(m.instClients[key]) > 0
}

// ClientFactory resolves a connector INSTANCE's own *Client — see
// InstanceClient. A function type, not an interface: internal/connector's
// RegisterExternalConnector needs nothing more than a closure to call once per
// configured instance, so it has no reason to import *Manager, and a test can
// supply a fake with no Manager behind it at all.
type ClientFactory func(instance string) (*Client, error)

// InstanceClientFactory binds InstanceClient to one plugin key as a
// ClientFactory, for RegisterExternalConnector's builder.
func (m *Manager) InstanceClientFactory(key string) ClientFactory {
	return func(instance string) (*Client, error) { return m.InstanceClient(key, instance) }
}

// InstanceClient returns the dedicated *Client that serves one configured
// connector INSTANCE of the plugin at key, constructing it (not yet started —
// every Client is lazy) on first use and reusing it after.
//
// Multi-instance isolation (docs/wiki/Plugins.md, docs/design/
// plugin-contract.md): by default, every configured instance of an external
// connector plugin gets its OWN subprocess — its own sandbox, scrubbed env,
// granted env, and staging subdirectory — rather than sharing the one process
// every plugin kind shared before this existed. host.state/host.auth/
// host.log's existing per-instance "active" scoping (Client.isActive) is now
// backed by a structural boundary (a sibling instance's requests never even
// reach this process) rather than bookkeeping alone.
//
// A plugin whose operator set shared_process: true on any configured instance
// (config.ConnectorRef.SharedProcess, unioned across instances in
// config.PluginRefs) instead shares the ONE persistent client Client(key)
// already holds — an explicit, documented resource trade-off for an operator
// running many instances of one plugin, at the cost of the isolation above.
//
// Meaningless for a runtime or engine key (handled by falling through to the
// persistent client the same as a SharedProcess connector, since neither has
// more than one "instance" sharing a Manager key) — only the connector
// builder path calls this.
func (m *Manager) InstanceClient(key, instance string) (*Client, error) {
	m.mu.RLock()
	spec, ok := m.specs[key]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", key)
	}
	if spec.Kind != KindConnector || spec.SharedProcess {
		c, ok := m.Client(key)
		if !ok {
			return nil, fmt.Errorf("plugin %q not found", key)
		}
		return c, nil
	}
	m.instMu.Lock()
	defer m.instMu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("plugin %q: manager is closed", key)
	}
	if m.instClients[key] == nil {
		m.instClients[key] = map[string]*Client{}
	}
	if c, ok := m.instClients[key][instance]; ok {
		return c, nil
	}
	instSpec := spec
	instSpec.Instance = instance
	c := NewClient(instSpec, m.deps)
	m.instClients[key][instance] = c
	return c, nil
}

// InstanceClients returns a snapshot of key's live per-instance clients,
// keyed by instance name. Empty for a key with no per-instance clients yet
// (nothing called InstanceClient for it) or one that uses the persistent
// client instead.
func (m *Manager) InstanceClients(key string) map[string]*Client {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	out := make(map[string]*Client, len(m.instClients[key]))
	for inst, c := range m.instClients[key] {
		out[inst] = c
	}
	return out
}

// LocalSnapshotShas returns the sha256 (hex, lowercase) of every LOCAL
// plugin's current resolution in this Manager — the local-build TOCTOU fix's
// content-addressed snapshot hash (SpecFromRef/snapshotLocal). A
// per-instance client always runs the same snapshot as its key's recorded
// Spec (they are resolved together, once, by SpecFromRef), so the type-level
// specs map alone is the complete set. A caller GC'ing
// LocalSnapshotRoot must union this across every Manager it built in this
// process before calling GCLocalSnapshots — removing a snapshot this Manager
// still depends on would break its NEXT respawn (never an already-running
// process, which keeps its already-open executable text regardless).
func (m *Manager) LocalSnapshotShas() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]bool{}
	for _, s := range m.specs {
		if s.Local && s.Sha256 != "" {
			out[s.Sha256] = true
		}
	}
	return out
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

// Reload swaps a plugin's subprocess(es) to newSpec's binary IN PLACE (see
// Client.Reload) and updates its recorded binary-identity fields. For a
// per-instance-isolated connector key that means EVERY live per-instance
// client — one moved plugin binary, every configured instance's process
// swapped — not just one; a runtime/engine/SharedProcess-connector key has
// just the one persistent client, as before multi-instance isolation. The
// same *Client pointer(s) stay in place, so every consumer keeps driving the
// new build with no re-registration. Client.Reload can block draining
// in-flight calls, so it runs OUTSIDE m.mu.
//
// Returns the first error Client.Reload reports (ErrReloadUnsupported/
// ErrReloadBusy) for the caller to map to a full restart. A per-instance key
// with more than one live client can therefore end up PARTIALLY swapped on
// error — some instances already on the new build, others not — which is
// safe only because the caller's one response to ANY Reload error is a full
// daemon restart (reloadMoved), which re-resolves every instance fresh; there
// is no caller that treats a Reload error as "retry later" while serving
// traffic from the partially-swapped set.
func (m *Manager) Reload(key string, newSpec Spec) error {
	var clients []*Client
	if c, ok := m.clients[key]; ok { // clients is immutable post-construction; no lock
		clients = append(clients, c)
	}
	m.instMu.Lock()
	for _, c := range m.instClients[key] {
		clients = append(clients, c)
	}
	m.instMu.Unlock()
	if len(clients) == 0 {
		return fmt.Errorf("plugin %q not found", key)
	}
	for _, c := range clients {
		// Client.Reload mutates only BinPath/Sha256/Resolved — a per-instance
		// client's own Spec.Instance tag (set at InstanceClient construction)
		// is untouched, so each keeps identifying itself correctly in logs
		// after the swap.
		if err := c.Reload(newSpec); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if s, ok := m.specs[key]; ok {
		s.BinPath, s.Sha256, s.Resolved = newSpec.BinPath, newSpec.Sha256, newSpec.Resolved
		m.specs[key] = s
	}
	m.mu.Unlock()
	return nil
}

// Close stops every plugin subprocess this Manager ever started: every
// persistent client, and every per-instance client InstanceClient created.
// Also marks the Manager closed so a later InstanceClient call (there should
// be none once the caller is shutting down, but a defensive guard costs
// nothing) creates no client this method will never reach.
func (m *Manager) Close() error {
	m.instMu.Lock()
	instClients := m.instClients
	m.instClients = nil
	m.closed = true
	m.instMu.Unlock()
	for _, c := range m.clients {
		_ = c.Close()
	}
	for _, byInst := range instClients {
		for _, c := range byInst {
			_ = c.Close()
		}
	}
	return nil
}

// StartAndDescribe starts a plugin's PERSISTENT client and returns its
// self-description — the runtime/engine/SharedProcess-connector registration
// path, where the client that describes itself is the same one that goes on
// to serve real traffic. A per-instance-isolated connector key has no
// persistent client to start; its type-level probe is ProbeDescribe instead,
// and each configured instance's own client is described separately
// (externalImpl's per-instance Q6 describe) once InstanceClient creates it.
//
// On any failure the client is left stopped and the error is returned (the
// caller disables that plugin's type, mirroring the connector convention of
// disable-not-crash).
func (m *Manager) StartAndDescribe(ctx context.Context, name string) (*Decl, error) {
	c, ok := m.clients[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	decl, err := startAndDescribe(ctx, c)
	if err != nil {
		return nil, err
	}
	m.recordDecl(name, decl)
	return decl, nil
}

// ProbeDescribe starts a THROWAWAY client for key's plugin — never retained —
// describes it, closes it, and returns the Decl. This is the connector
// type-level describe/install probe multi-instance isolation keeps as a
// single process (docs/wiki/Plugins.md "Multi-instance isolation"): the
// type-level Decl (kind, verbs, capabilities) is a property of the installed
// BINARY, not of any one configured instance, so there is nothing to gain
// from keeping this particular process running — each configured instance
// gets its own, separately, the first time InstanceClient is asked for it.
//
// Also records the boot surface into Decl(key) (same first-write-wins rule as
// StartAndDescribe), so hot-reload's SameReloadSurface comparison works the
// same for a per-instance connector as it always has for everything else.
func (m *Manager) ProbeDescribe(ctx context.Context, key string) (*Decl, error) {
	m.mu.RLock()
	spec, ok := m.specs[key]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", key)
	}
	c := NewClient(spec, m.deps)
	defer c.Close()
	decl, err := startAndDescribe(ctx, c)
	if err != nil {
		return nil, err
	}
	m.recordDecl(key, decl)
	return decl, nil
}

func startAndDescribe(ctx context.Context, c *Client) (*Decl, error) {
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	return c.Describe(ctx)
}

// recordDecl records name's Decl from its first describe (StartAndDescribe or
// ProbeDescribe) — the surface the daemon's registrations were built against
// — unless already recorded (idempotent: StartAndDescribe/ProbeDescribe may
// each be called more than once for the same key).
func (m *Manager) recordDecl(name string, decl *Decl) {
	m.mu.Lock()
	if _, seen := m.decls[name]; !seen {
		m.decls[name] = decl
	}
	m.mu.Unlock()
}

// Decl returns the self-description recorded at a plugin's first
// StartAndDescribe (the surface its registrations were built against), or nil.
func (m *Manager) Decl(name string) (*Decl, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.decls[name]
	return d, ok
}
