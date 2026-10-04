package plugin

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// Side-by-side plugin versions (docs/wiki/Plugins.md "Side-by-side
// versions") — the fix for the bug where plugins keyed only by "<kind>/<name>"
// let two connectors pinning different versions of the same plugin silently
// share whichever one's Use happened to be folded into the PluginRef first
// (config.PluginRefs, internal/config/use.go Use.InstallKey).
//
// config.PluginRefs still groups by name alone (one PluginRef per
// "<kind>/<name>", unchanged) — that is still the right shape for "the set
// of distinct plugin NAMES this config needs" (install/fetch/GC/CLI
// listing). But each configured connector INSTANCE now carries its OWN Use
// in ref.Instances[name].Use (config.ConnectorGrant), so no information is
// lost folding several instances into one ref.
//
// ExplodeRefs is the version-split step: given that name-grouped map plus
// local install state, it produces one entry per DISTINCT RESOLVED VERSION
// among a ref's instances — a process GROUP. Two instances whose
// constraints resolve to the same concrete build land in one group (and
// share its one process, exactly like multi-instance isolation's
// shared-by-default process always has); two instances resolving to
// different builds land in separate groups, each a distinct map entry with
// its own key, so everything downstream that already iterates "the derived
// plugin set" (Manager construction, Reconcile, loadConnectorPlugins,
// reload, update) sees N independent plugins instead of one that
// mysteriously serves N different binaries — no caller past this point ever
// needs to know "same name, different version" is even possible.
func ExplodeRefs(refs map[string]config.PluginRef, configDir string, state *InstallState) map[string]config.PluginRef {
	out := make(map[string]config.PluginRef, len(refs))
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ref := refs[key]
		groups := groupRef(ref, configDir, state)
		multi := len(groups) > 1
		for _, g := range groups {
			gkey := key
			if multi {
				gkey = key + "@" + g.discriminator
			}
			out[gkey] = g.ref
		}
	}
	return out
}

// resolvedGroup is one process group: ref narrowed to exactly the configured
// instances that resolved to the same concrete build (discriminator).
type resolvedGroup struct {
	ref           config.PluginRef
	discriminator string
}

// groupRef splits ref's configured instances into process groups by their
// OWN resolved version. A runtime or engine ref carries no per-instance
// multiplicity (config.PluginRefs never populates Instances for those — each
// is already exactly one configured thing), so it is always exactly one
// group: itself, resolved against install state via its own ref.Use instead
// of the (bare, pre-versioning) state.Get(key) a plain name lookup would
// give — the difference matters the moment TWO DIFFERENT PluginRefs of
// different names could never collide anyway, but it keeps a pinned
// runtime/engine's OWN constraint authoritative rather than "whatever
// happens to be the newest installed under this key" if a sibling config
// context ever installed a second version under the same name.
func groupRef(ref config.PluginRef, configDir string, state *InstallState) []resolvedGroup {
	if ref.Instances == nil {
		return []resolvedGroup{{ref: ref, discriminator: discriminatorFor(ref.Use, ref.Name, configDir, state)}}
	}

	names := make([]string, 0, len(ref.Instances))
	for n := range ref.Instances {
		names = append(names, n)
	}
	sort.Strings(names)

	type bucket struct {
		names []string
	}
	byDisc := map[string]*bucket{}
	var order []string
	for _, n := range names {
		u := ref.Instances[n].Use
		d := discriminatorFor(u, ref.Name, configDir, state)
		b, ok := byDisc[d]
		if !ok {
			b = &bucket{}
			byDisc[d] = b
			order = append(order, d)
		}
		b.names = append(b.names, n)
	}

	out := make([]resolvedGroup, 0, len(order))
	for _, d := range order {
		// Finding 2 (security): two instances that resolved to the SAME
		// version are not automatically one process — their isolation:
		// blocks must actually combine (config.CombineIsolation). A version
		// bucket whose non-isolated instances carry incompatible blocks
		// (e.g. a pin and a range landing on the same release, each
		// deny:true with different egress) is split further, by isolation
		// compatibility, so each incompatible instance gets its OWN process
		// running under exactly ITS OWN declared isolation — never a
		// silently-picked, conflict-dropping merge (narrowRef enforces this
		// as a hard invariant; see its comment).
		clusters := splitByIsolation(byDisc[d].names, ref.Instances)
		multiIso := len(clusters) > 1
		for i, names := range clusters {
			disc := d
			if multiIso {
				disc = fmt.Sprintf("%s~iso%d", d, i+1)
			}
			out = append(out, resolvedGroup{ref: narrowRef(ref, names), discriminator: disc})
		}
	}
	return out
}

// splitByIsolation partitions one resolved-version bucket's instance names
// into the fewest clusters whose SHARED (isolate: false) instances' isolation:
// blocks actually combine into one (config.CombineIsolation) — greedy
// first-fit over names in their given (sorted, so deterministic) order. An
// isolate: true instance never conflicts with anything here (it always gets
// its own process downstream regardless of which cluster carries it for
// bookkeeping), so every isolated instance is folded into cluster 0 — the
// one cluster that always exists, keeping the single-cluster case's output
// identical to before this split existed.
func splitByIsolation(names []string, instances map[string]config.ConnectorGrant) [][]string {
	type cluster struct {
		names []string
		iso   *config.IsolationConfig
	}
	var clusters []*cluster
	var isolated []string
	for _, n := range names {
		g := instances[n]
		if g.Isolate {
			isolated = append(isolated, n)
			continue
		}
		placed := false
		for _, c := range clusters {
			if merged, ok := config.CombineIsolation(c.iso, g.Isolation); ok {
				c.iso = merged
				c.names = append(c.names, n)
				placed = true
				break
			}
		}
		if !placed {
			clusters = append(clusters, &cluster{names: []string{n}, iso: g.Isolation})
		}
	}
	if len(clusters) == 0 {
		clusters = append(clusters, &cluster{})
	}
	clusters[0].names = append(clusters[0].names, isolated...)
	out := make([][]string, len(clusters))
	for i, c := range clusters {
		sort.Strings(c.names)
		out[i] = c.names
	}
	return out
}

// discriminatorFor is the concrete identity one `use:` reference resolves to
// right now, offline: a local dev build's content-addressed snapshot sha
// (two instances pointed at the same bytes share a group; different bytes —
// even under the same declared name — never do), the installed version a
// remote constraint resolves to (InstallState.GetForConstraint), or a
// "pending:<use>" bucket for a reference nothing installed satisfies yet —
// distinct unmet constraints get distinct buckets so each is tracked (and
// retried, and reported as not-installed) on its own rather than colliding
// into one.
func discriminatorFor(u config.Use, name, configDir string, state *InstallState) string {
	if u.Origin == config.OriginLocal {
		bin := u.Path
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(configDir, bin)
		}
		if _, sha, err := snapshotLocal(name, bin); err == nil {
			return "local:" + sha
		}
		return "local-err:" + bin
	}
	if inst, ok := state.GetForConstraint(u.InstallKey(), u); ok {
		return inst.Resolved
	}
	return "pending:" + u.String()
}

// narrowRef copies ref down to exactly the configured instances in names,
// recomputing the NON-ISOLATED union (Network/AllowSecrets/AllowEnv/
// Isolation) over only THOSE instances — never a sibling group's, which may
// run as an entirely separate process with its own union — and setting Use
// to one representative instance's own reference (every instance in names
// resolved to the identical concrete build, by construction, so which one's
// constraint TEXT is shown is cosmetic).
//
// Finding 2 (security): names is REQUIRED to already be isolation-compatible
// — groupRef's splitByIsolation is narrowRef's ONLY caller, and it never
// hands this function a set whose non-isolated instances' isolation: blocks
// fail to combine. If CombineIsolation ever disagrees here anyway, that is a
// bug in that pre-split, not a condition this function may paper over by
// silently keeping one side and dropping the other — it panics, loudly,
// naming both instances, rather than ever running one of them under
// isolation it did not declare (the vulnerability this fix closes).
func narrowRef(ref config.PluginRef, names []string) config.PluginRef {
	out := ref
	out.Instances = make(map[string]config.ConnectorGrant, len(names))
	out.Network, out.AllowSecrets, out.AllowEnv, out.Isolation = nil, nil, nil, nil
	rep := names[0]
	out.Use = ref.Instances[rep].Use
	out.Instance = rep
	isoFrom := ""
	for _, n := range names {
		g := ref.Instances[n]
		out.Instances[n] = g
		if g.Isolate {
			continue
		}
		out.Network = config.AppendUnique(out.Network, g.Network...)
		out.AllowSecrets = config.AppendUnique(out.AllowSecrets, g.AllowSecrets...)
		out.AllowEnv = config.AppendUnique(out.AllowEnv, g.AllowEnv...)
		if out.Isolation == nil && isoFrom == "" {
			out.Isolation, isoFrom = g.Isolation, n
			continue
		}
		merged, ok := config.CombineIsolation(out.Isolation, g.Isolation)
		if !ok {
			panic(fmt.Sprintf("plugin %s: instances %s and %s resolved to the same process group with isolation: blocks that do not combine — this must never happen (groupRef's isolation split is supposed to prevent it); this is a bug, not a config error", ref.Name, isoFrom, n))
		}
		out.Isolation = merged
	}
	return out
}
