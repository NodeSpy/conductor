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
// A key holds AT MOST ONE of two shapes of client, decided (usually) once at
// construction by the Spec's Kind and SharedProcess flag:
//
//   - a single, persistent, eagerly-created client in `clients` — every
//     runtime and engine key (neither has more than one "instance" sharing a
//     key), a connector key whose operator opted into shared_process:, and a
//     connector key whose plugin declares Capabilities.SingleProcess (read
//     from its recorded manifest — SpecFromRef);
//   - a lazily-created client PER CONFIGURED CONNECTOR INSTANCE, in
//     `instClients[key][instance]` (Manager.InstanceClient) — the default for
//     a connector key: multi-instance isolation (docs/wiki/Plugins.md) gives
//     each configured instance of an external plugin its own subprocess, its
//     own sandbox/env/staging dir, rather than sharing the type's one
//     process the way every plugin kind did before this existed.
//
// "Usually": a connector key built as the per-instance shape can be
// PROMOTED to the shared shape exactly once, by PromoteSharedProcess — the
// single_process capability's fallback for the one case SpecFromRef cannot
// see ahead of time (no manifest recorded it yet: a local development build,
// or the very first boot after install). Once promoted a key never reverts;
// see PromoteSharedProcess for the ordering this relies on.
//
// The connector TYPE-level describe/install probe (ProbeDescribe) uses
// neither of these: it is a throwaway client, started, described, and closed
// without ever being retained here — the type-level Decl is a property of the
// installed BINARY, not of any one configured instance, so there is nothing
// to keep running once it is known.
type Manager struct {
	// clients holds every KEY's persistent client — set (one entry per key
	// that gets one at all) at NewManager and, for the pointers themselves,
	// immutable from then on: a client's process is swapped IN PLACE by
	// Reload, never replaced by a different *Client. The one exception is a
	// brand-new ENTRY added by PromoteSharedProcess, after construction, for
	// a key that started with no persistent client at all — reads and this
	// one write path both go through instMu (see Client/HasLiveClient/
	// StartAndDescribe/PromoteSharedProcess), never the bare map.
	clients map[string]*Client
	order   []string // immutable after NewManager
	deps    Deps     // shared by every client this Manager ever creates, including lazily

	// mu guards specs + decls — the maps mutated after construction (Reload
	// updates a plugin's binary-identity fields; StartAndDescribe/ProbeDescribe
	// record a self-description; PromoteSharedProcess rewrites a key's Spec
	// to the shared shape). order is built once and read-only thereafter.
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

	// instMu guards instClients + closed + reloading — see the per-instance
	// shape above. Separate from mu: InstanceClient must not block a
	// concurrent Spec/Decl read (or vice versa) just because it is lazily
	// creating a client.
	instMu sync.Mutex
	// instClients holds each connector key's per-instance clients, created on
	// first InstanceClient call for that (key, instance) pair. nil entries are
	// never stored; an absent map for a key simply means no instance of it has
	// been asked for yet.
	instClients map[string]map[string]*Client
	// closed is set by Close so an InstanceClient call racing with shutdown
	// creates no client that Close would never reach.
	closed bool
	// reloading marks a key currently being Reload()ed — set before Reload
	// drains/swaps its snapshotted clients, cleared only after newSpec has
	// landed in specs (both under instMu). InstanceClient waits on
	// instCond while its key is set, rather than racing Reload: without
	// this, a brand-new instance first asked for in the window between
	// Reload's client snapshot and its specs[key] update would be built
	// from the PRE-reload spec (the OLD BinPath/Sha256) and then cached —
	// nothing ever revisits an already-created per-instance client, so
	// that instance would silently keep running the old binary forever,
	// even though Reload reported success and every OTHER instance (and a
	// fresh `plugin list`) shows the new one.
	reloading map[string]bool
	// instCond signals waiters blocked on `reloading` (bound to instMu).
	instCond *sync.Cond
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
//
// Grant fields (Network/AllowSecrets/AllowEnv/Isolation): for a connector
// ref that is NOT SharedProcess, the returned type-level Spec carries the
// MINIMUM, not the union ref carries — empty/nil. That type-level Spec is
// what Manager.ProbeDescribe runs the connector's throwaway type-level
// describe probe with, and the probe needs none of it: `plugin.describe` is
// a pure self-description, calling no verb and reading no instance
// connection (docs/wiki/Plugins.md "Security": "the install-time describe
// runs before any manifest exists, confined to nothing"). Granting it the
// union of every sibling instance's secrets/env/network would hand the
// probe process access nothing about it needs justifies. Each configured
// instance's OWN grant still travels, in ref.Instances, for
// Manager.InstanceClient to apply to that instance's own per-process Spec.
//
// A SharedProcess connector ref (or a runtime/engine ref, which was never
// unioned to begin with) keeps the union: one process then serves every
// instance, so it must be permitted whatever any of them declares.
//
// A plugin whose RECORDED manifest (inst.Manifest, from its last install-time
// describe) declares single_process: true (pkg/plugin/wire.go
// Capabilities.SingleProcess) gets exactly this same shared shape, even
// though the operator never set shared_process: — the capability overrides
// the operator's own setting, never the other way around. This is the
// steady-state path: the manifest is normally already on disk by the time a
// daemon boots (recorded at `plugin add`/`plugin update`/boot gap-fill,
// docs/wiki/Plugins.md "Multi-instance isolation"), so the Manager gets the
// right shape from construction, no live describe needed first. The one case
// this cannot see — the FIRST time this boot ever learns the capability (a
// local development build, which never gets a recorded manifest at all, or a
// remote plugin installed with no Describe step) — is handled by
// Manager.PromoteSharedProcess instead, called right after a live describe
// reveals it.
func SpecFromRef(ref config.PluginRef, configDir string, inst Installed, ok bool) Spec {
	shared := ref.SharedProcess || inst.Manifest.SingleProcess
	s := Spec{
		Name:          ref.Name,
		Kind:          Kind(ref.Kind()),
		Provides:      ref.Name,
		Version:       ref.Version(),
		TrustFull:     ref.TrustFull,
		SharedProcess: shared,
		Use:           ref.Use,
	}
	if s.Kind == KindConnector && !shared {
		// Per-instance isolation: the type-level Spec gets no grant of its
		// own (the minimum, for ProbeDescribe above); InstanceClient looks
		// each configured instance's grant up here instead. Probe marks this
		// as that type-level probe Spec explicitly (finding 2) — Network
		// being empty here means "deny all", not "unconfigured; fall back to
		// the plugin's declared egress" the way it would for an instance
		// that simply didn't set `network:`.
		s.Instances = ref.Instances
		s.Probe = true
	} else {
		s.Isolation = ref.Isolation
		s.IsolationDefaulted = ref.IsolationDefaulted
		s.Network = ref.Network
		s.AllowSecrets = ref.AllowSecrets
		s.AllowEnv = ref.AllowEnv
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

// InstanceSpec derives the per-process Spec for ONE configured connector
// instance from its plugin's type-level Spec (as SpecFromRef built it) —
// exactly the shape Manager.InstanceClient spawns that instance's own
// subprocess from. It is also what any OTHER caller that must probe one
// instance in its own process — cmd/conductor's per-instance reload re-probe
// (reload.go describeInstancesForInstall, finding 1) — uses, so both build an
// instance's process identically rather than one of them drifting.
//
// The instance's OWN grant travels — never a sibling's, and never the union
// (see SpecFromRef's doc comment): ghA's process gets exactly ghA's
// allow_env/network/allow_secrets/isolation. A key missing from
// spec.Instances (should not happen: config.PluginRefs populates one entry
// per configured instance) leaves the per-instance Spec at its zero grant —
// least privilege, not a silent widening. The returned Spec carries no
// Instances map of its own: there is nothing sibling to it once it names one
// instance.
func InstanceSpec(spec Spec, instance string) Spec {
	instSpec := spec
	instSpec.Instance = instance
	if g, ok := spec.Instances[instance]; ok {
		instSpec.Network = g.Network
		instSpec.AllowSecrets = g.AllowSecrets
		instSpec.AllowEnv = g.AllowEnv
		instSpec.Isolation = g.Isolation
	} else {
		instSpec.Network = nil
		instSpec.AllowSecrets = nil
		instSpec.AllowEnv = nil
		instSpec.Isolation = nil
	}
	instSpec.Instances = nil
	instSpec.Probe = false
	return instSpec
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
		reloading:   make(map[string]bool),
		deps:        deps,
	}
	m.instCond = sync.NewCond(&m.instMu)
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
// use InstanceClient for those. Goes through instMu — the same lock
// PromoteSharedProcess's one post-construction write to `clients` uses —
// rather than a bare map read, so the two can never race.
func (m *Manager) Client(name string) (*Client, bool) {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	c, ok := m.clients[name]
	return c, ok
}

// HasLiveClient reports whether key has at least one live *Client — its
// persistent one (Client), or any per-instance one InstanceClient has
// created. Where code used to check Client(key) alone to mean "is this plugin
// live", that is no longer sufficient for a (per-instance-isolated) connector
// key, which never gets a persistent client at all.
func (m *Manager) HasLiveClient(key string) bool {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	if _, ok := m.clients[key]; ok {
		return true
	}
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
	// Kind never changes after construction (Reload only ever moves
	// BinPath/Sha256/Resolved). SharedProcess normally doesn't either — the
	// one exception is PromoteSharedProcess (the single_process capability's
	// cold-start fallback), which can flip it false→true, exactly once,
	// before this key ever gets a per-instance client (it refuses to run
	// once one exists). So a spec that is ALREADY shared here is safe to
	// trust immediately; a spec that is NOT (yet) shared must be re-checked
	// after taking instMu below — the same lock PromoteSharedProcess holds
	// while it flips the flag — rather than racing ahead on this stale read.
	if spec.Kind != KindConnector || spec.SharedProcess {
		c, ok := m.Client(key)
		if !ok {
			return nil, fmt.Errorf("plugin %q not found", key)
		}
		return c, nil
	}
	m.instMu.Lock()
	defer m.instMu.Unlock()
	// A Reload for this key may be between its client snapshot and its
	// specs[key] update (or still draining/swapping the clients it
	// snapshotted) — wait it out rather than building a brand-new instance
	// from the PRE-reload spec. Once built and cached, a per-instance
	// client is never revisited, so racing ahead here would strand that one
	// instance on the old binary forever, even though Reload reports
	// success and every sibling instance is on the new build (finding 2).
	for m.reloading[key] && !m.closed {
		m.instCond.Wait()
	}
	if m.closed {
		return nil, fmt.Errorf("plugin %q: manager is closed", key)
	}
	// Re-read under instMu: PromoteSharedProcess may have landed between the
	// fast-path check above and this lock. A now-shared key falls through to
	// its (just-created) persistent client instead of starting a per-instance
	// one it would never need again.
	m.mu.RLock()
	spec = m.specs[key]
	m.mu.RUnlock()
	if spec.SharedProcess {
		if c, ok := m.clients[key]; ok {
			return c, nil
		}
		return nil, fmt.Errorf("plugin %q not found", key)
	}
	if m.instClients[key] == nil {
		m.instClients[key] = map[string]*Client{}
	}
	if c, ok := m.instClients[key][instance]; ok {
		return c, nil
	}
	c := NewClient(InstanceSpec(spec, instance), m.deps)
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

// RetouchLocalSnapshots refreshes the mtime of every local-build snapshot
// directory this Manager currently depends on (LocalSnapshotShas) — finding
// 5(d)'s daily keep-alive. touchSnapshotUsed otherwise only runs at a
// RESOLVE (SpecFromRef/snapshotLocal — boot, a reload) and at a SPAWN
// (Client.ensureLocked — including every crash-respawn), so a long-running
// daemon whose local plugin simply never crashes or reloads across many days
// never touches its snapshot again after boot. A SIBLING daemon's
// GCLocalSnapshotsOld assumes nothing goes that long between touches —
// DefaultLocalSnapshotGrace (7 days) is generous, but "this plugin has been
// rock-solid for a week" should never be the reason its snapshot looks
// abandoned to another process sharing the state dir. The caller
// (cmd/conductor's periodic retouch loop) is expected to call this roughly
// daily for as long as the daemon runs.
func (m *Manager) RetouchLocalSnapshots() {
	root := LocalSnapshotRoot()
	if root == "" {
		return
	}
	for sha := range m.LocalSnapshotShas() {
		touchSnapshotUsed(filepath.Join(root, sha))
	}
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
//
// Races with a brand-new instance's FIRST InstanceClient call (finding 2):
// key is marked `reloading` (under instMu) before the snapshot below, and
// stays marked for this whole call — including the slow, unlocked
// client.Reload drain/swap loop and the final specs[key] update — so
// InstanceClient blocks (on instCond) rather than building that new
// instance's process from the pre-reload spec and caching it forever on the
// old binary. The mark is cleared (and waiters woken) only once specs[key]
// itself reflects newSpec, so a woken InstanceClient's re-read is always
// current.
func (m *Manager) Reload(key string, newSpec Spec) error {
	m.instMu.Lock()
	if m.reloading[key] {
		m.instMu.Unlock()
		return fmt.Errorf("plugin %q: a reload is already in progress", key)
	}
	m.reloading[key] = true
	var clients []*Client
	if c, ok := m.clients[key]; ok { // guarded by instMu (PromoteSharedProcess's one write path too)
		clients = append(clients, c)
	}
	for _, c := range m.instClients[key] {
		clients = append(clients, c)
	}
	m.instMu.Unlock()

	finish := func(err error) error {
		m.instMu.Lock()
		delete(m.reloading, key)
		m.instCond.Broadcast()
		m.instMu.Unlock()
		return err
	}

	if len(clients) == 0 {
		return finish(fmt.Errorf("plugin %q not found", key))
	}
	for _, c := range clients {
		// Client.Reload mutates only BinPath/Sha256/Resolved — a per-instance
		// client's own Spec.Instance tag (set at InstanceClient construction)
		// is untouched, so each keeps identifying itself correctly in logs
		// after the swap.
		if err := c.Reload(newSpec); err != nil {
			return finish(err)
		}
	}
	m.mu.Lock()
	if s, ok := m.specs[key]; ok {
		s.BinPath, s.Sha256, s.Resolved = newSpec.BinPath, newSpec.Sha256, newSpec.Resolved
		m.specs[key] = s
	}
	m.mu.Unlock()
	return finish(nil)
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
	// Wake anything blocked in InstanceClient waiting on a Reload for some
	// key: Close itself does not clear `reloading` (that is Reload's own
	// job, still in flight on its own goroutine), but a waiter re-checks
	// m.closed every time it wakes, so this just avoids it sitting idle an
	// extra beat once Reload's own finish() broadcast would otherwise be
	// the only thing to wake it.
	m.instCond.Broadcast()
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
	c, ok := m.Client(name)
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

// PromoteSharedProcess is the single_process capability's COLD-START
// fallback (SpecFromRef is the steady-state path, reading it from a
// RECORDED manifest): the first time this boot's own live describe reveals
// Capabilities.SingleProcess on a key that was built as the default
// per-instance shape (no manifest had recorded it yet — a local development
// build, which never gets one at all, or a remote plugin's very first
// install), this flips that key to the shared shape it should have had from
// construction, and returns the one persistent client every configured
// instance will now share.
//
// sharedSpec is the caller's responsibility to build correctly: it must be
// key's existing type-level Spec with SharedProcess set true and reshaped
// exactly like SpecFromRef's own shared branch — Instances cleared, Probe
// cleared, Isolation/Network/AllowSecrets/AllowEnv set to the connector
// ref's UNIONED grant (the same fields a shared_process: true ref's Spec
// already carries) — so the one process this starts is permitted whatever
// any of its instances needs, per docs/wiki/Plugins.md "Multi-instance
// isolation". PromoteSharedProcess itself does not spawn anything —
// construction is lazy, same as everywhere else in this package; the caller
// still calls StartAndDescribe(ctx, key) afterward to actually start it (and
// get its live Decl) through the now-persistent client this created.
//
// Refuses once a per-instance client already exists for key (len(instClients
// [key]) > 0): folding live, already-split per-instance processes back into
// one — stopping some, keeping another, redirecting in-flight calls — is a
// disruptive operation this does not attempt; the caller is expected to
// treat that refusal as a plugin problem (disable the type, log loudly,
// exactly as every other describe-time failure already does) rather than
// retry. In the one caller today (cmd/conductor's loadConnectorPlugins) this
// can never actually happen: the type-level describe that discovers the
// capability runs BEFORE RegisterExternalConnector hands out the
// ClientFactory that is the only way a per-instance client ever gets
// created, so there is nothing to fold yet.
//
// Idempotent: calling it again for an already-promoted key (one that
// already has a persistent client) just returns that same client — a
// double call (which does not happen on the one caller's path, but costs
// nothing to make safe) never spawns a second process.
func (m *Manager) PromoteSharedProcess(key string, sharedSpec Spec) (*Client, error) {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("plugin %q: manager is closed", key)
	}
	if c, ok := m.clients[key]; ok {
		return c, nil
	}
	if len(m.instClients[key]) > 0 {
		return nil, fmt.Errorf("plugin %q: cannot switch to a single shared process — %d per-instance client(s) are already live", key, len(m.instClients[key]))
	}
	m.mu.Lock()
	if _, ok := m.specs[key]; !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("plugin %q not found", key)
	}
	m.specs[key] = sharedSpec
	m.mu.Unlock()
	c := NewClient(sharedSpec, m.deps)
	m.clients[key] = c
	return c, nil
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
//
// The returned Decl may say Capabilities.SingleProcess — this throwaway
// process is still the right one to learn that from (it is a pure
// self-description either way), but the caller must act on it: it means
// this key should not stay per-instance-isolated after all. See
// PromoteSharedProcess, which the caller invokes next in that case.
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
