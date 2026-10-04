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
// A key holds up to two shapes of client at once, decided by the Spec's Kind
// and the per-instance isolate: setting (docs/wiki/Plugins.md "Multi-instance
// isolation"):
//
//   - a single, persistent, eagerly-created SHARED client in `clients` —
//     every runtime and engine key (neither has more than one "instance"
//     sharing a key), and a connector key with at least one configured
//     instance that did NOT set isolate: true (Spec.Shared, the default:
//     every such instance shares this one process, with the union of their
//     grants);
//   - a lazily-created client PER ISOLATED CONFIGURED CONNECTOR INSTANCE, in
//     `instClients[key][instance]` (Manager.InstanceClient) — only for an
//     instance that set isolate: true, confined to exactly its own grant.
//
// A connector key whose plugin declares Capabilities.SingleProcess cannot be
// split at all: an isolated instance of it is refused (ForbidIsolated) rather
// than ever folded into the shared client silently.
//
// The connector TYPE-level describe/install probe (ProbeDescribe) uses
// neither of these: it is a throwaway client, started, described, and closed
// without ever being retained here, used only when a key has NO shared
// client at all (every configured instance isolates) — the type-level Decl
// is a property of the installed BINARY, not of any one configured instance,
// so there is nothing to gain from keeping this particular process running.
// When a key DOES have a shared client, that client's own StartAndDescribe
// IS the type-level describe; no separate probe process is spun up for it.
type Manager struct {
	// clients holds every KEY's persistent SHARED client — set (one entry
	// per key that has at least one non-isolated instance) at NewManager
	// and, for the pointers themselves, immutable from then on: a client's
	// process is swapped IN PLACE by Reload, never replaced by a different
	// *Client.
	clients map[string]*Client
	order   []string // immutable after NewManager
	deps    Deps     // shared by every client this Manager ever creates, including lazily

	// mu guards specs + decls — the maps mutated after construction (Reload
	// updates a plugin's binary-identity fields; StartAndDescribe/ProbeDescribe
	// record a self-description). order is built once and read-only
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

	// instMu guards instClients + forbidden + closed + reloading — see the
	// per-instance shape above. Separate from mu: InstanceClient must not
	// block a concurrent Spec/Decl read (or vice versa) just because it is
	// lazily creating a client.
	instMu sync.Mutex
	// instClients holds each connector key's ISOLATED instances' clients,
	// created on first InstanceClient call for that (key, instance) pair.
	// nil entries are never stored; an absent map for a key simply means no
	// isolated instance of it has been asked for yet.
	instClients map[string]map[string]*Client
	// forbidden records an isolated instance ForbidIsolated has refused — the
	// single_process capability discovered only at a live describe, after
	// the operator already configured isolate: true on it (loadConnectorPlugins).
	// InstanceClient refuses with the recorded reason instead of ever
	// building that instance's own process.
	forbidden map[string]map[string]string
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
// Grant fields (Network/AllowSecrets/AllowEnv/Isolation): the returned
// type-level Spec carries the UNION across every NON-ISOLATED configured
// instance (config.PluginRef's own Network/AllowSecrets/AllowEnv/Isolation,
// already unioned that way by config.PluginRefs) — what the ONE SHARED
// process (Spec.Shared) must be permitted, since it serves every instance
// that did not opt into isolate: true. Each configured instance's OWN grant
// still travels separately, in ref.Instances (every instance, isolated or
// not), for Manager.InstanceClient/InstanceSpec to apply to an isolated
// instance's own per-process Spec, or to a reload per-instance recheck probe
// (cmd/conductor's describeInstancesForInstall) for any instance regardless
// of shape.
//
// When EVERY configured instance of a connector ref isolates
// (!ref.HasSharedInstance()), there is no shared process to grant anything
// to or to describe itself — the type-level Spec instead gets the MINIMUM
// (empty/nil), and Probe is set: that is what Manager.ProbeDescribe runs the
// connector's throwaway type-level describe probe with, and the probe needs
// none of it: `plugin.describe` is a pure self-description, calling no verb
// and reading no instance connection (docs/wiki/Plugins.md "Security": "the
// install-time describe runs before any manifest exists, confined to
// nothing"). Granting it the union of every sibling instance's secrets/env/
// network would hand the probe process access nothing about it needs
// justifies.
func SpecFromRef(ref config.PluginRef, configDir string, inst Installed, ok bool) Spec {
	shared := ref.HasSharedInstance()
	s := Spec{
		Name:      ref.Name,
		Kind:      Kind(ref.Kind()),
		Provides:  ref.Name,
		Version:   ref.Version(),
		TrustFull: ref.TrustFull,
		Shared:    shared,
		Use:       ref.Use,
	}
	if s.Kind == KindConnector {
		s.Instances = ref.Instances
	}
	if s.Kind == KindConnector && !shared {
		// No shared process exists for this key (every configured instance
		// isolates): the type-level Spec gets no grant of its own (the
		// minimum, for ProbeDescribe above). Probe marks this as that
		// type-level probe Spec explicitly (finding 2) — Network being empty
		// here means "deny all", not "unconfigured; fall back to the
		// plugin's declared egress" the way it would for an instance that
		// simply didn't set `network:`.
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

// NewManager builds a Manager from the config's derived plugin set. plugins
// MUST already be exploded into one entry per process GROUP (ExplodeRefs) —
// one per distinct resolved version among a plugin's configured instances,
// not one per plugin NAME (docs/wiki/Plugins.md "Side-by-side versions"); a
// name with two groups therefore occupies two entries, each its own fully
// independent plugin as far as Manager is concerned. configDir is the
// directory the config file lives in (for resolving local paths); state
// supplies each group's installed binary, resolved against that GROUP's own
// Use (ref.Use, already narrowed to one representative instance's
// constraint by ExplodeRefs — every instance in the group resolved to the
// same concrete build, so any of their constraints finds the same install
// record). deps is shared by every client, including one created later by
// InstanceClient. It does not start any subprocess.
//
// A runtime or engine key, and a connector key with at least one non-isolated
// configured instance (Spec.Shared), get their shared client HERE, eagerly.
// A connector key whose EVERY instance isolates gets none yet: InstanceClient
// creates its isolated instances' clients lazily, on the builder's first call
// for each.
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
		var spec Spec
		if ref.Use.Origin == config.OriginLocal {
			spec = SpecFromRef(ref, configDir, Installed{}, false)
		} else {
			inst, ok := state.GetForConstraint(ref.Use.InstallKey(), ref.Use)
			spec = SpecFromRef(ref, configDir, inst, ok)
		}
		spec.GroupKey = key
		m.specs[key] = spec
		if spec.Shared {
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

// Client returns a plugin's PERSISTENT SHARED client — the one every runtime
// and engine key has, and the one a connector key with at least one
// non-isolated configured instance has (Spec.Shared). A connector key whose
// EVERY configured instance isolates has none: use InstanceClient for those.
// Goes through instMu rather than a bare map read, so a reader can never
// observe a half-constructed map.
func (m *Manager) Client(name string) (*Client, bool) {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	c, ok := m.clients[name]
	return c, ok
}

// HasLiveClient reports whether key has at least one live *Client — its
// shared one (Client), or any isolated-instance one InstanceClient has
// created. Where code used to check Client(key) alone to mean "is this plugin
// live", that is no longer sufficient for a connector key whose every
// instance isolates, which never gets a shared client at all.
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

// InstanceClient returns the *Client that serves one configured connector
// INSTANCE of the plugin at key: the key's one SHARED client, unless this
// particular instance opted into isolate: true, in which case it gets its own
// dedicated client, constructed (not yet started — every Client is lazy) on
// first use and reused after.
//
// Multi-instance isolation (docs/wiki/Plugins.md, docs/design/
// plugin-contract.md): by DEFAULT, every configured instance of an external
// connector plugin SHARES its plugin name+version's one process — the union
// of every non-isolated instance's grant. An instance that sets isolate:
// true instead gets its OWN subprocess — its own sandbox, scrubbed env,
// granted env, and staging subdirectory, confined to exactly its own grant —
// trading the lower resource cost of sharing for the isolation of a process
// boundary: a crash, a hang, or a crash-loop in it never touches the shared
// process or a sibling isolated instance, and host.state/host.auth/
// host.log's existing per-instance "active" scoping (Client.isActive) is
// backed by a structural boundary (the shared client's requests never
// reach this instance's process, and vice versa) rather than bookkeeping
// alone — see ForbidIsolated for the one case an isolated instance is
// refused outright instead (its plugin cannot be split into more than one
// process at all).
//
// Meaningless for a runtime or engine key (handled by falling through to the
// shared client, since neither has more than one "instance" sharing a
// Manager key, so spec.Instances is always nil for them) — only the
// connector builder path calls this.
func (m *Manager) InstanceClient(key, instance string) (*Client, error) {
	m.mu.RLock()
	spec, ok := m.specs[key]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", key)
	}
	if spec.Kind != KindConnector || !spec.Instances[instance].Isolate {
		// The shared path: every runtime/engine key, and any connector
		// instance that did not ask for its own process. Isolate is fixed by
		// config at construction (Reload only ever moves
		// BinPath/Sha256/Resolved), so this read is safe without instMu.
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
	// The single_process capability discovered only at a live describe
	// (ForbidIsolated, cmd/conductor's loadConnectorPlugins): this instance
	// asked for its own process, but the plugin cannot be split into more
	// than one at all — refuse it with the recorded reason rather than ever
	// silently falling back to the shared client it never agreed to share
	// grants with.
	if reason, ok := m.forbidden[key][instance]; ok {
		return nil, fmt.Errorf("plugin %q instance %q: %s", key, instance, reason)
	}
	if m.instClients[key] == nil {
		m.instClients[key] = map[string]*Client{}
	}
	if c, ok := m.instClients[key][instance]; ok {
		return c, nil
	}
	// Re-read under m.mu: a Reload that just finished (the wait above) has
	// updated specs[key]'s BinPath/Sha256/Resolved, and this may be this
	// instance's very FIRST client — build it from the CURRENT spec, never
	// the stale one read before the wait, or it would be cached on the old
	// binary forever (finding 2).
	m.mu.RLock()
	spec = m.specs[key]
	m.mu.RUnlock()
	c := NewClient(InstanceSpec(spec, instance), m.deps)
	m.instClients[key][instance] = c
	return c, nil
}

// ForbidIsolated refuses isolate: true for one configured connector instance
// of key, recording reason so any (current or later) InstanceClient call for
// it fails fast instead of ever building its own process. This is the
// single_process capability's LATE-discovery path: its live describe
// revealed Capabilities.SingleProcess only now — no recorded manifest said so
// ahead of time, which is instead a hard config validation error naming the
// connector and plugin BEFORE anything spawns (cmd/conductor's
// loadConnectorPlugins checks the recorded manifest for that case). "Never
// silently share": an instance that asked for isolation is refused, loud,
// rather than folded into the shared client.
func (m *Manager) ForbidIsolated(key, instance, reason string) {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	if m.forbidden == nil {
		m.forbidden = map[string]map[string]string{}
	}
	if m.forbidden[key] == nil {
		m.forbidden[key] = map[string]string{}
	}
	m.forbidden[key][instance] = reason
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
// Client.Reload) and updates its recorded binary-identity fields: the key's
// shared client (if it has one) AND every live isolated-instance client —
// one moved plugin binary, every process it runs as swapped, not just one. A
// runtime/engine key, or a connector key with no isolated instance, has just
// the one shared client, as before multi-instance isolation existed. The
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
	if c, ok := m.clients[key]; ok { // guarded by instMu
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

// StartAndDescribe starts a plugin's SHARED client and returns its
// self-description — the runtime/engine/connector-with-a-shared-process
// registration path, where the client that describes itself is the same one
// that goes on to serve real (non-isolated) traffic. A connector key whose
// every instance isolates has no shared client to start; its type-level
// probe is ProbeDescribe instead, and each isolated instance's own client is
// described separately (externalImpl's per-instance Q6 describe) once
// InstanceClient creates it.
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

// ProbeDescribe starts a THROWAWAY client for key's plugin — never retained —
// describes it, closes it, and returns the Decl. This is the connector
// type-level describe/install probe multi-instance isolation keeps as a
// single process (docs/wiki/Plugins.md "Multi-instance isolation"), used only
// when key has NO shared client (every configured instance isolates — Spec.
// Shared is false): the type-level Decl (kind, verbs, capabilities) is a
// property of the installed BINARY, not of any one configured instance, so
// there is nothing to gain from keeping this particular process running —
// each isolated instance gets its own, separately, the first time
// InstanceClient is asked for it. When key DOES have a shared client,
// StartAndDescribe describes THAT one instead — it already holds no
// instance's traffic until one calls it, so there is nothing left for a
// separate throwaway probe to buy.
//
// Also records the boot surface into Decl(key) (same first-write-wins rule as
// StartAndDescribe), so hot-reload's SameReloadSurface comparison works the
// same regardless of which path recorded it.
//
// The returned Decl may say Capabilities.SingleProcess — this throwaway
// process is still the right one to learn that from (it is a pure
// self-description either way). The caller (cmd/conductor's
// loadConnectorPlugins) acts on it: every one of this key's (necessarily all
// isolated, since there is no shared client) configured instances is then
// refused via ForbidIsolated rather than silently started anyway.
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
