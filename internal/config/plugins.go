package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// PluginRef is one external plugin the daemon must run out-of-process. It is
// DERIVED, never authored: there is no `plugins:` block. Every entry here comes
// from a `use:` reference on a connectors:/runtimes: entry that did not resolve
// to a builtin (see PluginRefs).
//
// The kind is the block the reference appeared in — a connector can never be
// wired as a runtime — and is re-checked against the plugin's own `describe` at
// install/start time.
type PluginRef struct {
	// Name is the implementation name: the connector type it registers, or the
	// runtime name agents select with `runtime:`. It is the `use:` reference's
	// resolved leaf, not the config map key.
	Name string
	// Instance is the connectors:/runtimes: map key that referenced it. Several
	// instances may share one plugin; the first in sorted order names it here.
	Instance string
	// Use is the parsed reference — where the implementation comes from.
	Use Use
	// Isolation is OPTIONAL hardening carried from the referencing entry. nil
	// is the normal case: the default model is the permission manifest, not OS
	// confinement.
	Isolation *IsolationConfig
	// IsolationDefaulted marks an Isolation that conductor SYNTHESIZED (an
	// untrusted-by-default code engine), not one the operator wrote. Its
	// enforcement is BEST-EFFORT: if the OS sandbox can't be applied the launch
	// degrades to a warning, whereas an operator-written block fails closed.
	IsolationDefaulted bool
	// TrustFull marks an engine the operator opted out of sandboxing
	// (`engines.<name>.trust: full`). Isolation is nil and the "no OS sandbox"
	// warning is suppressed — they chose it deliberately.
	TrustFull bool
	// Network is the referencing connector's declared egress. Empty for a
	// runtime (a runtime executes your agents; its egress is not narrowed).
	Network []string
	// AllowSecrets optionally tightens which secret refs may cross to the
	// plugin (exact names, no globs).
	AllowSecrets []string
	// AllowEnv are the daemon environment variables the operator granted the
	// plugin (connectors.<name>.allow_env), within what it declares.
	AllowEnv []string
	// Instances carries each CONFIGURED CONNECTOR INSTANCE's own grants —
	// Network/AllowSecrets/AllowEnv/Isolation/Isolate exactly as that one
	// connectors: entry declared them, never unioned with a sibling
	// instance's. Keyed by the connectors: map name. Populated only for a
	// connector-kind ref (nil for a runtime/engine ref, which has no
	// "several instances of one plugin" multiplicity to begin with).
	//
	// The Network/AllowSecrets/AllowEnv/Isolation fields ABOVE are the UNION
	// across every NON-ISOLATED (Isolate: false, the default) instance of
	// this plugin — that union is what the ONE SHARED process needs (it
	// serves every instance that did not opt into isolate: true, so it must
	// be permitted whatever any of them declares). An isolated instance's own
	// grant never joins that union; it travels only in THIS map, which
	// internal/plugin.Manager's per-instance client path (still the existing
	// machinery, just reserved for isolate: true now instead of being the
	// default) confines that one instance's own process to — ghA's own
	// process gets exactly ghA's own allow_env/network/allow_secrets/
	// isolation, never ghB's, never the shared union — see
	// docs/wiki/Plugins.md "Multi-instance isolation".
	Instances map[string]ConnectorGrant
}

// ConnectorGrant is one configured connector instance's own sandbox/
// isolation grant — Network, AllowSecrets, AllowEnv, Isolation and Isolate
// exactly as that connectors: entry declared them. PluginRef.Instances
// carries one of these per configured instance so a per-instance plugin
// process (internal/plugin.Manager's isolated-instance path) can be confined
// to exactly its own instance's grant instead of the shared union every
// non-isolated sibling instance shares.
type ConnectorGrant struct {
	Network      []string
	AllowSecrets []string
	AllowEnv     []string
	Isolation    *IsolationConfig
	// Isolate mirrors ConnectorRef.Isolate: true means this one configured
	// instance gets its own dedicated process instead of sharing the
	// plugin's one default process.
	Isolate bool
	// Use is THIS instance's own resolved `use:` reference, version
	// constraint included — never a sibling's, and never collapsed to
	// whichever instance PluginRefs happened to see first (the side-by-side
	// versions fix, docs/wiki/Plugins.md "Side-by-side versions"). Two
	// instances naming the same plugin NAME but different `@version`
	// constraints are folded into one PluginRef by name (PluginRefs), but
	// each keeps its OWN Use here — internal/plugin resolves each instance's
	// own constraint against install state and groups instances by their
	// RESOLVED concrete version, not by this shared PluginRef alone, so
	// `use: github@v1` on one connector and `use: github@v2` on another each
	// run their own version's process instead of one silently winning.
	Use Use
}

// PluginKind values, retained as the wire/CLI spelling of UseKind.
const (
	PluginKindConnector = string(UseKindConnector)
	PluginKindRuntime   = string(UseKindRuntime)
	PluginKindEngine    = string(UseKindEngine)
)

// Kind is what this plugin provides, derived from the block it was referenced
// from.
func (p PluginRef) Kind() string { return string(p.Use.Kind) }

// Key is the plugin's stable identity in install state: "<kind-dir>/<name>".
func (p PluginRef) Key() string { return p.Use.InstallKey() }

// IsRemote reports whether the plugin must be fetched from a forge (as opposed
// to a local development binary).
func (p PluginRef) IsRemote() bool { return p.Use.IsRemote() }

// Source is the canonical source string for the trust allowlist — "" for a
// local binary, which is not fetched.
func (p PluginRef) Source() string { return p.Use.Source() }

// Version is the `@…` constraint from the reference, if any.
func (p PluginRef) Version() string { return p.Use.Version }

// Ref is the `name@version` attribution carried on audit records and shown by
// `plugin list`.
func (p PluginRef) Ref() string {
	if p.Use.Version == "" {
		return p.Name
	}
	return p.Name + "@" + p.Use.Version
}

// IsolatedInstanceNames lists the configured connector instances of this
// plugin that opted into their own process (isolate: true), sorted. Empty for
// a runtime/engine ref, or a connector ref with no isolated instance.
func (p PluginRef) IsolatedInstanceNames() []string {
	var out []string
	for name, g := range p.Instances {
		if g.Isolate {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// SharedInstanceNames lists the configured connector instances that share the
// plugin's default process (isolate: false — every instance not in
// IsolatedInstanceNames), sorted.
func (p PluginRef) SharedInstanceNames() []string {
	var out []string
	for name, g := range p.Instances {
		if !g.Isolate {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// HasSharedInstance reports whether at least one configured instance shares
// the plugin's default process — the condition under which that one shared
// process is actually started. A connector ref whose EVERY instance isolates
// has no shared process at all. Always true for a runtime/engine ref (p.
// Instances is nil for those — they have no several-instances-of-one-plugin
// shape to isolate in the first place, so there is nothing to range over and
// len(p.Instances) == 0 falls through to "shared").
func (p PluginRef) HasSharedInstance() bool {
	if p.Kind() != PluginKindConnector {
		return true
	}
	if len(p.Instances) == 0 {
		return true
	}
	for _, g := range p.Instances {
		if !g.Isolate {
			return true
		}
	}
	return false
}

// mergeIsolationBestEffort combines two non-isolated instances' isolation:
// blocks for PluginRefs' running union. It is best-effort: a genuine conflict
// (two different, incompatible blocks) is reported precisely by
// validatePluginRefs/checkIsolationMerge, which runs after PluginRefs and
// gates whether the config loads at all — this just must never panic or
// silently invent a block neither instance wrote.
func mergeIsolationBestEffort(a, b *IsolationConfig) *IsolationConfig {
	merged, ok := combineIsolation(a, b)
	if ok {
		return merged
	}
	// Conflicting: keep the first seen so a caller that (incorrectly) never
	// checked validatePluginRefs' error still gets SOME deterministic block
	// rather than a nil that reads as "no isolation at all".
	if a != nil {
		return a
	}
	return b
}

// combineIsolation combines two non-isolated instances' isolation: blocks
// into the one block their shared process runs under. nil combines with
// anything (one instance wrote no block; the other's stands). Two written
// blocks combine only when every field EXCEPT Network is identical: mode,
// container image/engine, limits, privileged and allow_root are a choice of
// WHICH sandbox shape to run, not a point on a shared strictness scale — mode:
// namespace and mode: container are simply different, not comparable, so a
// mismatch there is a conflict (reported as a config error), never resolved
// by picking one. Network is the one dimension that DOES have a natural join,
// the same "widen, never narrow a grant into something neither side asked
// for" shape Network/AllowSecrets/AllowEnv already use, but only while
// neither instance denies: two advisory egress lists union, since the shared
// process must be permitted whatever any of its instances needs. `deny: true`
// is a promise of no network beyond the instance's own allowlist, so a
// sibling must make the SAME promise (deny, with the same egress) to share
// its process. Otherwise the sibling's egress would open a path the denying
// instance never asked for, or the deny would cut the sibling's network. That
// mismatch is a conflict (a missing block counts as no deny); isolate: true
// gives either instance its own process.
func combineIsolation(a, b *IsolationConfig) (*IsolationConfig, bool) {
	if a == nil || b == nil {
		other := a
		if other == nil {
			other = b
		}
		if other != nil && other.Network != nil && other.Network.Deny {
			return nil, false // one denies, the other wrote no block: no deny
		}
		return other, true
	}
	ac, bc := *a, *b
	ac.Network, bc.Network = nil, nil
	if !reflect.DeepEqual(ac, bc) {
		return nil, false
	}
	net, ok := combineIsolationNetwork(a.Network, b.Network)
	if !ok {
		return nil, false
	}
	merged := *a
	merged.Network = net
	return &merged, true
}

// combineIsolationNetwork joins two network blocks for one shared process:
// advisory egress lists union; a deny must be matched exactly (both deny,
// with the same egress), or the instances can't share a process.
func combineIsolationNetwork(a, b *IsolationNetwork) (*IsolationNetwork, bool) {
	aDeny, bDeny := a != nil && a.Deny, b != nil && b.Deny
	if aDeny || bDeny {
		if !aDeny || !bDeny || !sameSet(a.Egress, b.Egress) {
			return nil, false
		}
		return a, true
	}
	if a == nil {
		return b, true
	}
	if b == nil {
		return a, true
	}
	return &IsolationNetwork{Egress: appendUnique(append([]string(nil), a.Egress...), b.Egress...)}, true
}

// sameSet reports whether two string lists hold the same members.
func sameSet(a, b []string) bool {
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	other := map[string]bool{}
	for _, x := range b {
		other[x] = true
	}
	for _, x := range a {
		if !other[x] {
			return false
		}
	}
	return true
}

// PluginRefs derives the set of external plugins this config needs: every
// connectors:/runtimes: entry whose `use:` did not resolve to a builtin, keyed
// by "<kind-dir>/<name>" so a connector and a runtime of the same name never
// collide. Entries whose `use:` does not parse are skipped — validateConnectors
// reports those as config errors.
//
// Two instances may legitimately share one plugin (two Jira connectors, one
// jira plugin). They are folded into a single entry; a second instance naming
// the SAME name from a DIFFERENT source is reported by validatePluginRefs.
func (c *Config) PluginRefs() map[string]PluginRef {
	out := map[string]PluginRef{}

	names := make([]string, 0, len(c.ConnectorsMap))
	for n := range c.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := c.ConnectorsMap[name]
		u, err := ref.Resolved()
		if err != nil || u.IsBuiltin() {
			continue
		}
		p, seen := out[u.InstallKey()]
		if !seen {
			p = PluginRef{Name: u.Name, Instance: name, Use: u, Instances: map[string]ConnectorGrant{}}
		}
		// Hardening and declared egress union across NON-ISOLATED instances
		// only: the DEFAULT shared process runs once for every instance that
		// did not ask for its own (isolate: true), so it must be permitted
		// whatever any of THOSE declares — never an isolated sibling's,
		// which never joins this union (p.Instances[name] below carries that
		// instance's own grant instead, and nothing else reads it). A
		// conflicting pair of isolation: blocks across non-isolated
		// instances is a load error (validatePluginRefs); this best-effort
		// merge takes the first and lets that later, authoritative check
		// report it precisely.
		if !ref.Isolate {
			p.Isolation = mergeIsolationBestEffort(p.Isolation, ref.Isolation)
			p.Network = appendUnique(p.Network, ref.Network...)
			p.AllowSecrets = appendUnique(p.AllowSecrets, ref.AllowSecrets...)
			p.AllowEnv = appendUnique(p.AllowEnv, ref.AllowEnv...)
		}
		p.Instances[name] = ConnectorGrant{
			Network:      append([]string(nil), ref.Network...),
			AllowSecrets: append([]string(nil), ref.AllowSecrets...),
			AllowEnv:     append([]string(nil), ref.AllowEnv...),
			Isolation:    ref.Isolation,
			Isolate:      ref.Isolate,
			Use:          u,
		}
		out[u.InstallKey()] = p
	}

	rnames := make([]string, 0, len(c.Runtimes))
	for n := range c.Runtimes {
		rnames = append(rnames, n)
	}
	sort.Strings(rnames)
	for _, name := range rnames {
		rt := c.Runtimes[name]
		u, err := rt.Resolved()
		if err != nil || u.IsBuiltin() {
			continue
		}
		// A runtime's map key IS the runtime name agents select, so it wins over
		// the reference leaf (`runtimes: { modal: { use: acme/p/conductor-modal } }`
		// is selected as `runtime: modal`).
		u.Name = name
		p := PluginRef{Name: name, Instance: name, Use: u, Isolation: rt.Isolation}
		out[u.InstallKey()] = p
	}

	// ENGINES have no block of their own for the REFERENCE: a step's `use:` IS
	// the reference, read off the steps themselves. But a code engine runs
	// arbitrary code and declares no capabilities, so — unlike a connector the
	// operator deliberately wired to a service — it is UNTRUSTED by default and
	// gets a synthesized OS sandbox unless the operator tunes/opts out via the
	// `engines:` block (keyed by engine name).
	c.WalkSteps(func(_ IdentityScope, _ int, s *Step) {
		sel, class := s.StepEngine()
		if class != EnginePlugin {
			return
		}
		u, err := ParseUse(UseKindEngine, sel)
		if err != nil || u.IsBuiltin() {
			return // validateStepEngine reports an unparseable reference
		}
		if _, seen := out[u.InstallKey()]; seen {
			return
		}
		p := PluginRef{Name: u.Name, Instance: sel, Use: u}
		ec := c.Engines[u.Name]
		p.Network = ec.Network
		p.AllowSecrets = ec.AllowSecrets
		switch {
		case ec.TrustFull():
			p.TrustFull = true // unconfined, deliberately — stay quiet
		case ec.Isolation != nil:
			p.Isolation = ec.Isolation // explicit → fail-closed
		default:
			p.Isolation = defaultEngineIsolation() // untrusted default → best-effort
			p.IsolationDefaulted = true
		}
		out[u.InstallKey()] = p
	})
	return out
}

// defaultEngineIsolation is the sandbox an untrusted-by-default code engine gets
// when the operator sets nothing: a user+pid+mount namespace with the network
// denied. A code engine is pure computation over inputs delivered on its RPC
// transport, so denying egress and masking the daemon's own state/config costs
// it nothing while turning its "no declared capabilities" into a kernel-enforced
// wall. Enforcement is best-effort (see PluginRef.IsolationDefaulted).
func defaultEngineIsolation() *IsolationConfig {
	return &IsolationConfig{
		Mode:    "namespace",
		Network: &IsolationNetwork{Deny: true},
	}
}

// defaultPackCodeIsolation is the sandbox a pack-authored cli/host code step
// gets when it declares none (confined-by-default, §15): the same
// least-privilege namespace jail an untrusted engine gets — pid-isolated,
// network denied, and (via the pivot_root allow-list) only the workdir and the
// step's own code/ctx temp dirs visible, the daemon's state/config hidden by
// absence. A pack that legitimately needs the network or an external path
// declares its own isolation: with `network`/`fs`; one the operator fully
// trusts opts out with `trust: full`. Best-effort (Step.IsolationDefaulted).
func defaultPackCodeIsolation() *IsolationConfig {
	return &IsolationConfig{
		Mode:    "namespace",
		Network: &IsolationNetwork{Deny: true},
	}
}

// PluginRefsComplete reports whether PluginRefs may be treated as the COMPLETE
// desired set — the question anything that PRUNES install state has to answer
// before it removes a record.
//
// It is false for a config that DECLARES `packs:` but has not had them
// instantiated. A pack's steps only reach c.Workflows/c.Triggers/c.Checks at
// instantiate (see instantiatePacks), and a step's `use:`/`run:` IS the engine
// reference — so before that point a pack-internal `run: js` is invisible to
// PluginRefs and the derived set is a subset, not the answer. Pruning against a
// subset drops plugins that are very much still needed.
//
// Load always instantiates (and hard-fails if it cannot), so a loaded config is
// complete. This exists so a caller that did NOT go through Load cannot
// silently get a destructive prune. Nothing here touches the network:
// instantiation reads the already-vendored pack tree.
func (c *Config) PluginRefsComplete() bool {
	return len(c.Packs) == 0 || c.packsInstantiated
}

// validatePluginRefs checks the derived plugin set for the conflicts the
// per-entry validation cannot see: two connectors claiming the same
// implementation name from different sources would silently route one
// instance's credentials to the other's binary.
func (c *Config) validatePluginRefs() error {
	bySource := map[string]string{} // "<kind>/<name>" -> source, first wins
	byInstance := map[string]string{}

	check := func(kind UseKind, instance, ref string) error {
		u, err := ParseUse(kind, ref)
		if err != nil || u.IsBuiltin() {
			return nil
		}
		key := u.InstallKey()
		src := u.Source()
		if src == "" {
			src = "local:" + u.Path
		}
		if prev, ok := bySource[key]; ok && prev != src {
			return errConflict(kind, u.Name, byInstance[key], prev, instance, src)
		}
		bySource[key], byInstance[key] = src, instance
		return nil
	}

	names := make([]string, 0, len(c.ConnectorsMap))
	for n := range c.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := check(UseKindConnector, n, c.ConnectorsMap[n].Use); err != nil {
			return err
		}
	}
	rnames := make([]string, 0, len(c.Runtimes))
	for n := range c.Runtimes {
		rnames = append(rnames, n)
	}
	sort.Strings(rnames)
	for _, n := range rnames {
		if err := check(UseKindRuntime, n, c.Runtimes[n].Use); err != nil {
			return err
		}
	}
	// Engine references live on steps; two steps naming the same engine from
	// different sources is the same conflict a pair of connectors would be —
	// one binary would serve both, and which one is whichever step loaded
	// first.
	var engErr error
	c.WalkSteps(func(_ IdentityScope, _ int, s *Step) {
		sel, class := s.StepEngine()
		if class != EnginePlugin || engErr != nil {
			return
		}
		engErr = check(UseKindEngine, sel, sel)
	})
	if engErr != nil {
		return engErr
	}
	return c.checkIsolationMerge()
}

// checkIsolationMerge is the OFFLINE half of the isolation-merge check
// PluginRefs' own merge (mergeIsolationBestEffort) defers to: every
// NON-ISOLATED instance that shares one process must have isolation: blocks
// that actually combine into a single block that process can run under
// (combineIsolation) — a plugin with two non-isolated instances declaring
// mode: namespace and mode: container, say, cannot be satisfied by any one
// process. The fix the error names is either make the blocks match or set
// isolate: true on one of them (giving it, and the conflict, its own process).
//
// Side-by-side versions (docs/wiki/Plugins.md): config validation runs
// before install state is ever consulted, so it cannot know which CONCRETE
// version two differently-WRITTEN `use:` constraints resolve to — only
// whether they are the identical reference text, which is always the same
// version. Grouping here is therefore by (key, raw `use:` text), not by key
// alone: two instances with the exact same pin (or the exact same unpinned
// reference) are PROVABLY one process and are checked here, offline, for a
// fast load-time error; two instances whose constraints merely MIGHT
// resolve to the same version (two different ranges that happen to pick the
// same release) are deferred to the authoritative check internal/plugin
// runs once install state is joined and the real grouping is known
// (cmd/conductor's checkIsolateAgainstKnownSingleProcess's sibling for
// isolation, loadConnectorPlugins).
func (c *Config) checkIsolationMerge() error {
	type entry struct {
		instance  string
		isolation *IsolationConfig
	}
	byKey := map[string][]entry{}
	names := make([]string, 0, len(c.ConnectorsMap))
	for n := range c.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := c.ConnectorsMap[name]
		if ref.Isolate {
			continue
		}
		u, err := ref.Resolved()
		if err != nil || u.IsBuiltin() {
			continue
		}
		groupKey := u.InstallKey() + "\x00" + u.String()
		byKey[groupKey] = append(byKey[groupKey], entry{name, ref.Isolation})
	}
	for groupKey, entries := range byKey {
		if len(entries) < 2 {
			continue
		}
		key, _, _ := strings.Cut(groupKey, "\x00")
		combined := entries[0].isolation
		combinedFrom := entries[0].instance
		for _, e := range entries[1:] {
			merged, ok := combineIsolation(combined, e.isolation)
			if !ok {
				name := strings.TrimPrefix(key, UseKindConnector.Dir()+"/")
				return fmt.Errorf("config: connectors: %s and %s share the %s plugin %s's one process by default, but declare different isolation: blocks that cannot combine — make them match, or set isolate: true on one to give it its own process",
					combinedFrom, e.instance, key, name)
			}
			combined = merged
		}
	}
	return nil
}

func errConflict(kind UseKind, name, aInst, aSrc, bInst, bSrc string) error {
	return &conflictError{kind: kind, name: name, aInst: aInst, aSrc: aSrc, bInst: bInst, bSrc: bSrc}
}

type conflictError struct {
	kind                     UseKind
	name                     string
	aInst, aSrc, bInst, bSrc string
	_                        struct{}
}

func (e *conflictError) Error() string {
	return "config: " + e.kind.Block() + ": " + e.aInst + " and " + e.bInst +
		" both resolve to the " + string(e.kind) + " " + e.name +
		", but from different sources (" + e.aSrc + " vs " + e.bSrc +
		") — two implementations cannot share a name; rename one, or point both at the same source"
}

// CombineIsolation is the exported form of combineIsolation — internal/plugin
// reuses it (ExplodeRefs/narrowRef) to recompute a process GROUP's isolation
// union over only the configured instances that group actually serves, once
// Manager construction knows which instances share one resolved version.
func CombineIsolation(a, b *IsolationConfig) (*IsolationConfig, bool) { return combineIsolation(a, b) }

// AppendUnique is the exported form of appendUnique, reused for the same
// reason as CombineIsolation.
func AppendUnique(dst []string, add ...string) []string { return appendUnique(dst, add...) }

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		dup := false
		for _, d := range dst {
			if d == a {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, a)
		}
	}
	return dst
}

// isHexSHA256 reports whether s is exactly 64 lowercase/uppercase hex digits.
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
