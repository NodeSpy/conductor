package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// Reconcile — the app-extension model's declarative resolve step
// (docs/design/use-unification.md §C).
//
// The config is the DESIRED set: every non-builtin `use:` reference names an
// implementation that must be installed. Reconcile brings the local install
// state to that set. It is the plugin analogue of config.ResolvePacks, and is
// what `conductor init`, `conductor plugin update`, and the boot gap-fill all
// call — with different Options, not different code paths.

// Action names what Reconcile did to one reference.
const (
	// ActionInstalled — it was referenced and not installed; it is now.
	ActionInstalled = "installed"
	// ActionUpdated — it was installed at a different build; it moved.
	ActionUpdated = "updated"
	// ActionCurrent — already at the build the reference resolves to.
	ActionCurrent = "up-to-date"
	// ActionPinned — an exact @version pin, already satisfied; left alone.
	ActionPinned = "pinned"
	// ActionLocal — a local development binary; nothing to fetch.
	ActionLocal = "local"
	// ActionSkipped — a gaps-only pass left an already-installed plugin alone.
	ActionSkipped = "skipped"
	// ActionFailed — resolution failed; see Err.
	ActionFailed = "failed"
)

// Resolution is one reference's reconcile outcome, for logging and the CLI.
type Resolution struct {
	// Key is the plugin's plain install-state identity, "<kind-dir>/<name>"
	// — shared by EVERY group of this plugin regardless of resolved version
	// (install state keys its records by Key+Resolved, never GroupKey).
	Key string
	// GroupKey is the key this resolution was reconciled UNDER in the refs
	// map Reconcile was called with — the plain Key when the plugin has only
	// one resolved-version group (the common case, and every runtime/
	// engine), or "<Key>@<resolved>" when more than one group is configured
	// side by side (docs/wiki/Plugins.md "Side-by-side versions"). A caller
	// that drives the Manager/registry for this specific group (reload,
	// loadConnectorPlugins) keys off GroupKey; one that means "the plugin,
	// regardless of version" (GC, `plugin list`) keys off Key.
	GroupKey string
	Name     string
	Kind     string
	Use      string // the reference as written
	Origin   string
	Action   string
	Tag      string // resolved release tag
	Sha      string // verified binary sha
	PrevSha  string // the sha this replaced, when it changed
	Path     string // installed binary path
	Manifest Manifest
	Err      error
}

// Changed reports whether this resolution moved anything on disk.
func (r Resolution) Changed() bool {
	return r.Action == ActionInstalled || r.Action == ActionUpdated
}

// DescribeFunc spawns a freshly-installed plugin once and returns its
// self-description, so the permission manifest and the declared kind are
// recorded at install time — known before the plugin runs on any later boot,
// and re-checked against the block that referenced it. Nil skips the step (the
// install still happens; the manifest is simply empty).
type DescribeFunc func(ctx context.Context, spec Spec) (*Decl, error)

// Options tune one reconcile pass.
type Options struct {
	// Only limits the pass to a single implementation name (`plugin update <name>`).
	Only string
	// GapsOnly installs what is missing and leaves everything installed alone.
	// This is the BOOT posture: never re-resolve on the hot path.
	GapsOnly bool
	// Prune authorizes dropping install-state records the refs map does not
	// name. It is OPT-IN because the prune is only correct when refs is the
	// COMPLETE desired set — which is a promise only the CALLER can make.
	//
	// Reconcile cannot tell a whole-config refs map from a deliberately partial
	// one (`plugin add` resolves exactly one reference), and guessing wrong
	// uninstalls working plugins. In particular an ENGINE used only inside a
	// pack is absent from any refs map built without instantiating that pack,
	// so an unguarded prune silently removes it and the next `validate` fails
	// with "plugin js: not installed". Leave this false unless refs came from
	// config.PluginRefs on a config whose PluginRefsComplete reports true.
	Prune bool
	// Force re-resolves even an exact pin (`plugin update --force`).
	Force bool
	// AllowUnlisted bypasses the plugin-source trust allowlist.
	AllowUnlisted bool
	// Describe records the permission manifest at install (see DescribeFunc).
	Describe DescribeFunc
	// Log receives one line per install/update so every change is visible.
	Log func(string, ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// Reconcile brings install state in line with the config's referenced
// plugins. refs is the NAME-grouped map (config.PluginRefs' output, or an
// ExplodeRefs'd map when a caller already has one — see below): Reconcile
// does its OWN per-instance exploding internally now, so it is no longer the
// caller's job to pre-group by resolved version before calling this.
//
// Side-by-side versions, finding 1: a ref with Instances populated (a
// connector referenced by more than one connectors: entry) is resolved ONE
// CONFIGURED INSTANCE AT A TIME, each against its OWN `use:` constraint —
// never a single representative's, which is what let a pinned instance
// silently get dragged onto whatever version an unpinned sibling moved to,
// and then get GC'd out from under itself the moment that sibling advanced
// (see reconcileInstances). Two instances whose constraint TEXT is
// byte-identical trivially resolve identically; two instances whose
// constraints merely happen to resolve to the same release are also fetched
// and described only ONCE (finding 4) — see reconcileInstances' bucket
// cache. A ref with Instances == nil (a runtime/engine, or a single ad-hoc
// entry like `plugin add`'s) has no such multiplicity and is resolved
// exactly as before, via reconcileOne.
//
// ListTags is memoized per RemoteSource for the span of this one pass
// (pinnedTagCache): every instance resolving its own constraint needs the
// same tag list again and again, and it cannot change mid-pass.
//
// It is DEGRADE-SAFE: a reference that cannot be fetched (network down, release
// missing) is recorded as failed and reconcile CONTINUES, so one unreachable
// plugin never blocks the others and never takes the daemon down. The returned
// error is non-nil only when the state file itself could not be written; the
// caller reports per-reference failures from the Resolutions.
func Reconcile(refs map[string]config.PluginRef, state *InstallState, trust *config.PackTrustConfig, api ReleaseAPI, opts Options) ([]Resolution, error) {
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	api = &pinnedTagCache{api: api, tags: map[string][]string{}}

	var (
		results []Resolution
		dirty   bool
	)
	keep := map[VersionKey]bool{}
	addKeep := func(res Resolution) {
		if res.Tag != "" {
			keep[VersionKey{Key: res.Key, Resolved: res.Tag}] = true
		}
	}
	for _, key := range keys {
		ref := refs[key]
		if opts.Only != "" && opts.Only != ref.Name && opts.Only != key {
			continue
		}
		if ref.Instances == nil {
			res, changed := reconcileOne(key, ref, state, trust, api, opts)
			dirty = dirty || changed
			results = append(results, res)
			addKeep(res)
			continue
		}
		sub, changed := reconcileInstances(key, ref, state, trust, api, opts)
		dirty = dirty || changed
		for _, res := range sub {
			results = append(results, res)
			addKeep(res)
		}
	}

	// Drop install-state records no group in refs resolves to any more, so
	// install state does not accumulate forever. The BINARY is left on disk
	// for a plain whole-key removal only when `plugin remove` asks for it;
	// GCVersions here removes a SPECIFIC version's own directory, since that
	// version itself is genuinely gone from the desired set (a config change
	// moved every instance of it off that version) rather than merely
	// unmentioned.
	//
	// Only ever prune what we can PROVE is unreferenced: the caller must opt in
	// (see Options.Prune), the pass must not be scoped to one plugin, and a
	// gaps-only pass touches nothing. A partial refs map is a subset of the
	// desired set, and "absent from a subset" is not evidence of "unused".
	if opts.Prune && opts.Only == "" && !opts.GapsOnly {
		dropped, err := state.GCVersions(keep)
		if err != nil {
			opts.logf("plugin install state: GC: %v", err)
		}
		for _, vk := range dropped {
			dirty = true
			opts.logf("plugin %s@%s: no longer referenced by the config — dropped from install state", vk.Key, vk.Resolved)
		}
	}
	if dirty {
		if err := state.Save(); err != nil {
			return results, err
		}
	}
	return results, nil
}

func reconcileOne(groupKey string, ref config.PluginRef, state *InstallState, trust *config.PackTrustConfig, api ReleaseAPI, opts Options) (Resolution, bool) {
	key := ref.Use.InstallKey()
	res := Resolution{
		Key: key, GroupKey: groupKey, Name: ref.Name, Kind: ref.Kind(),
		Use: ref.Use.String(), Origin: string(ref.Use.Origin),
	}

	// A local development binary is not fetched, verified against a release, or
	// pinned — it is whatever the operator built. Record it so `plugin list`
	// can show it, and move on.
	if ref.Use.Origin == config.OriginLocal {
		res.Action, res.Path = ActionLocal, ref.Use.Path
		return res, false
	}

	prev, installed := state.GetForConstraint(key, ref.Use)

	if !opts.AllowUnlisted && !trust.PluginSourceAllowed(ref.Source()) {
		res.Action = ActionFailed
		res.Err = fmt.Errorf("plugin %s: source %q is not trusted — add it to plugin_trust.allow, or re-run with --allow-unlisted (the official repo %s needs no entry)", ref.Name, ref.Source(), config.OfficialSource)
		return res, false
	}

	// Stay-current is the default; an exact @version pin is the opt-out. A
	// gaps-only pass (boot) leaves anything installed exactly where it is.
	if installed {
		if opts.GapsOnly {
			res.Action, res.Tag, res.Sha, res.Path = ActionSkipped, prev.Resolved, prev.Sha256, prev.Path
			res.Manifest = prev.Manifest
			return res, false
		}
		if ref.Use.IsPinned() && !opts.Force && prev.Use == ref.Use.String() && binaryPresent(prev.Path) {
			res.Action, res.Tag, res.Sha, res.Path = ActionPinned, prev.Resolved, prev.Sha256, prev.Path
			res.Manifest = prev.Manifest
			return res, false
		}
	}

	rs := RemoteSource{URL: ref.Use.GitURL(), Component: ref.Use.Component}
	cacheDirFor := func(tag string) string { return BinDirForVersion(state.Dir(), key, tag) }
	binPath, tag, sha, verified, err := FetchRemoteVerified(rs, ref.Use.Version, "", cacheDirFor, api)
	if err != nil {
		res.Action, res.Err = ActionFailed, fmt.Errorf("plugin %s: %w", ref.Name, err)
		if installed {
			// Degrade, do not fail: keep running the build we already have.
			res.Tag, res.Sha, res.Path, res.Manifest = prev.Resolved, prev.Sha256, prev.Path, prev.Manifest
			opts.logf("plugin %s: could not reach %s (%v) — keeping the installed build %s (%s)", ref.Name, ref.Source(), err, prev.Resolved, shortSha(prev.Sha256))
		}
		return res, false
	}

	res.Tag, res.Sha, res.Path = tag, sha, binPath
	switch {
	case installed && prev.Sha256 == sha && prev.Resolved == tag:
		res.Action, res.Manifest = ActionCurrent, prev.Manifest
		// Nothing moved, but the reference text may have changed (a widened
		// constraint), or this fetch verified a build an older record did not
		// mark verified; keep the record honest.
		if prev.Use != ref.Use.String() || prev.ReleaseVerified != verified {
			prev.Use = ref.Use.String()
			prev.ReleaseVerified = verified
			state.Put(prev)
			return res, true
		}
		return res, false
	case installed:
		res.Action, res.PrevSha = ActionUpdated, prev.Sha256
	default:
		res.Action = ActionInstalled
	}

	// Record the permission manifest (and the declared kind) by describing the
	// build we just installed — BEFORE it is ever asked to do work, and while
	// the operator is watching the install.
	rec := Installed{
		Key: key, Kind: ref.Kind(), Name: ref.Name,
		Use: ref.Use.String(), Source: ref.Source(),
		Resolved: tag, Sha256: sha, Path: binPath, ReleaseVerified: verified,
	}
	if opts.Describe != nil {
		spec := SpecFromRef(ref, "", rec, true)
		decl, derr := opts.Describe(context.Background(), spec)
		if derr != nil {
			res.Action, res.Err = ActionFailed, fmt.Errorf("plugin %s: installed %s but it could not describe itself: %w", ref.Name, tag, derr)
			return res, false
		}
		if err := checkDeclKind(ref, decl); err != nil {
			res.Action, res.Err = ActionFailed, err
			return res, false
		}
		rec.Manifest = manifestFromDecl(decl)
	}
	res.Manifest = rec.Manifest
	state.Put(rec)

	if res.Action == ActionUpdated {
		opts.logf("plugin %s: updated %s -> %s (sha %s -> %s); permissions: %s",
			ref.Name, prev.Resolved, tag, shortSha(prev.Sha256), shortSha(sha), rec.Manifest.Summary())
	} else {
		opts.logf("plugin %s: installed %s from %s (sha %s); permissions: %s",
			ref.Name, tag, ref.Source(), shortSha(sha), rec.Manifest.Summary())
	}
	return res, true
}

// pinnedTagCache memoizes ReleaseAPI.ListTags by RemoteSource for the span of
// one Reconcile pass. The tag list cannot change mid-pass, so when several
// configured instances of one plugin each resolve their own constraint
// (finding 1), listing it once per source — not once per instance — is
// simply correct, and saves the repeat network round trip finding 4 flags.
type pinnedTagCache struct {
	api  ReleaseAPI
	tags map[string][]string
}

func (c *pinnedTagCache) ListTags(rs RemoteSource) ([]string, error) {
	k := rs.URL + "\x00" + rs.Component
	if tags, ok := c.tags[k]; ok {
		return tags, nil
	}
	tags, err := c.api.ListTags(rs)
	if err != nil {
		return nil, err
	}
	c.tags[k] = tags
	return tags, nil
}

func (c *pinnedTagCache) Download(rs RemoteSource, tag, file, destDir string) (string, error) {
	return c.api.Download(rs, tag, file, destDir)
}

// sortedInstanceNames lists m's keys, sorted.
func sortedInstanceNames(m map[string]config.ConnectorGrant) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// reconcileInstances is Reconcile's per-instance explode for a ref with
// Instances populated (finding 1) — see Reconcile's doc comment. Two phases:
//
//  1. Resolve + fetch: every configured instance resolves its OWN `use:`
//     constraint to a release tag; every DISTINCT tag among them is fetched
//     (downloaded, checksum-verified) at most ONCE this pass (finding 4) —
//     two instances whose constraint TEXT differs but who land on the
//     identical release never download it twice. The fetched build is
//     recorded into state immediately (sha/path, manifest still empty) so
//     phase 2's grouping sees it as installed.
//
//  2. Group + describe: groupRef — the SAME function ExplodeRefs uses to
//     build the Manager's process groups — is run against the now-updated
//     state, so the groups (and GroupKeys) this function reports agree with
//     what a LATER, fresh ExplodeRefs over the same state independently
//     computes (reload.go's moved-plugin lookup depends on that agreement).
//     groupRef also applies the isolation-conflict split (finding 2), so an
//     update that newly collides two instances' isolation: blocks at one
//     resolved version is reported as two groups here too. Each DISTINCT
//     resolved tag is described at most once (finding 4) — reused verbatim
//     across an isolation-split sibling group at the identical tag, since a
//     plugin's declared capabilities do not depend on which sandbox wraps
//     the describe-time probe.
func reconcileInstances(groupKeyIn string, ref config.PluginRef, state *InstallState, trust *config.PackTrustConfig, api ReleaseAPI, opts Options) ([]Resolution, bool) {
	// key is the plugin's install-state identity — ALWAYS ref.Use.InstallKey(),
	// never the map key this function was called under. The two happen to be
	// identical for the common, un-exploded caller (cfg.PluginRefs()'s plain
	// "<kind-dir>/<name>" keys), but a caller that already exploded before
	// calling Reconcile (a legacy/defensive shape, since Reconcile no longer
	// needs that) hands in a SUFFIXED map key ("<key>@<discriminator>") —
	// using that for install-state reads/writes would silently install under
	// a key nothing else looks up records by.
	key := ref.Use.InstallKey()
	names := sortedInstanceNames(ref.Instances)
	perInstance := make(map[string]Resolution, len(names))
	dirty := false

	// Snapshot what this key had installed BEFORE this pass touches
	// anything, so every instance landing on the same tag classifies
	// Installed/Updated/Current consistently (never one instance "updated"
	// and an identically-resolving sibling "current" just because of
	// iteration order).
	prevVersions := state.AllVersions(key)
	hadAnyPrev := len(prevVersions) > 0
	prevByTag := make(map[string]Installed, len(prevVersions))
	var prevRepSha string
	for _, p := range prevVersions {
		prevByTag[p.Resolved] = p
		prevRepSha = p.Sha256 // AllVersions is sorted ascending; last wins
	}

	type bucketResult struct {
		binPath, sha string
		verified     bool
		err          error
	}
	buckets := map[string]*bucketResult{}

	for _, n := range names {
		g := ref.Instances[n]
		u := g.Use
		instRef := ref
		instRef.Use = u
		instRef.Instance = n
		base := Resolution{Key: key, Name: ref.Name, Kind: ref.Kind(), Use: u.String(), Origin: string(u.Origin)}

		if u.Origin == config.OriginLocal {
			base.Action, base.Path = ActionLocal, u.Path
			perInstance[n] = base
			continue
		}

		if !opts.AllowUnlisted && !trust.PluginSourceAllowed(instRef.Source()) {
			base.Action = ActionFailed
			base.Err = fmt.Errorf("plugin %s: source %q is not trusted — add it to plugin_trust.allow, or re-run with --allow-unlisted (the official repo %s needs no entry)", ref.Name, instRef.Source(), config.OfficialSource)
			perInstance[n] = base
			continue
		}

		prev, installed := state.GetForConstraint(key, u)
		if installed {
			if opts.GapsOnly {
				base.Action, base.Tag, base.Sha, base.Path, base.Manifest = ActionSkipped, prev.Resolved, prev.Sha256, prev.Path, prev.Manifest
				perInstance[n] = base
				continue
			}
			// A pin is satisfied by the RESOLVED record, not by matching Use
			// TEXT: GetForConstraint already only returns a record whose
			// Resolved version satisfies u's own constraint (an exact pin
			// matches only that literal tag), so once such a record exists
			// and its binary is on disk, this instance's pin is met —
			// full stop. The record's Use field is per-(Key,Resolved), not
			// per-instance: when a pinned instance shares a resolved version
			// with an unpinned (or differently-worded) sibling, whichever
			// instance last wrote the record leaves ITS OWN text there, which
			// need not equal this instance's. Requiring text equality here
			// (as before) made a pinned instance's "already satisfied" check
			// flap based on sibling iteration order, forcing it through a
			// full network re-fetch on every single reconcile pass forever —
			// the HIGH-severity finding this comment replaces.
			if u.IsPinned() && !opts.Force && binaryPresent(prev.Path) {
				base.Action, base.Tag, base.Sha, base.Path, base.Manifest = ActionPinned, prev.Resolved, prev.Sha256, prev.Path, prev.Manifest
				perInstance[n] = base
				continue
			}
		}

		rs := RemoteSource{URL: u.GitURL(), Component: u.Component}
		tag, tagErr := CheckFetchable(rs, u.Version, api)
		if tagErr != nil {
			base.Action, base.Err = ActionFailed, fmt.Errorf("plugin %s: %w", ref.Name, tagErr)
			if installed {
				base.Tag, base.Sha, base.Path, base.Manifest = prev.Resolved, prev.Sha256, prev.Path, prev.Manifest
				opts.logf("plugin %s: could not reach %s (%v) — keeping the installed build %s (%s)", ref.Name, instRef.Source(), tagErr, prev.Resolved, shortSha(prev.Sha256))
			}
			perInstance[n] = base
			continue
		}
		base.Tag = tag

		b, ok := buckets[tag]
		if !ok {
			b = &bucketResult{}
			buckets[tag] = b
			cacheDirFor := func(t string) string { return BinDirForVersion(state.Dir(), key, t) }
			binPath, _, sha, verified, ferr := FetchRemoteVerified(rs, u.Version, "", cacheDirFor, api)
			if ferr != nil {
				b.err = fmt.Errorf("plugin %s: %w", ref.Name, ferr)
			} else {
				b.binPath, b.sha, b.verified = binPath, sha, verified
			}
		}
		if b.err != nil {
			base.Action, base.Err = ActionFailed, b.err
			if installed {
				base.Tag, base.Sha, base.Path, base.Manifest = prev.Resolved, prev.Sha256, prev.Path, prev.Manifest
				opts.logf("plugin %s: could not reach %s (%v) — keeping the installed build %s (%s)", ref.Name, instRef.Source(), b.err, prev.Resolved, shortSha(prev.Sha256))
			}
			perInstance[n] = base
			continue
		}

		base.Sha, base.Path = b.sha, b.binPath
		if prevForTag, had := prevByTag[tag]; had && prevForTag.Sha256 == b.sha {
			base.Action, base.Manifest = ActionCurrent, prevForTag.Manifest
			// Never rewrite the shared (Key, Resolved) record's Use just
			// because THIS instance's constraint text differs from
			// whatever is already stored there — that text belongs to
			// whichever instance's fetch originally installed or last
			// moved this version (the ActionInstalled/ActionUpdated branch
			// below), and rewriting it here on every pass a sibling with
			// different wording happens to be processed is exactly the
			// flip-flop the pinned-instance bug above depended on. Only
			// ReleaseVerified is honest to update here: this fetch may have
			// verified a build an older record did not mark verified.
			if prevForTag.ReleaseVerified != b.verified {
				prevForTag.ReleaseVerified = b.verified
				state.Put(prevForTag)
				dirty = true
			}
		} else if hadAnyPrev {
			base.Action, base.PrevSha = ActionUpdated, prevRepSha
		} else {
			base.Action = ActionInstalled
		}
		if base.Action == ActionInstalled || base.Action == ActionUpdated {
			rec := Installed{Key: key, Kind: ref.Kind(), Name: ref.Name, Use: u.String(), Source: instRef.Source(), Resolved: tag, Sha256: b.sha, Path: b.binPath, ReleaseVerified: b.verified}
			if existing, ok := state.GetVersion(key, tag); ok {
				rec.Manifest = existing.Manifest
			}
			state.Put(rec)
			dirty = true
		}
		perInstance[n] = base
	}

	// Phase 2 — group the now-fully-resolved instances by their OWN
	// resolved identity (groupRef, finding 1), describing each DISTINCT
	// release at most once (finding 4).
	groups := groupRef(ref, "", state)
	multi := len(groups) > 1
	type described struct {
		manifest Manifest
		err      error
	}
	describeCache := map[string]*described{}

	out := make([]Resolution, 0, len(groups))
	for _, g := range groups {
		gkey := groupKeyIn
		if multi {
			gkey = groupKeyIn + "@" + g.discriminator
		}
		groupNames := sortedInstanceNames(g.ref.Instances)
		if len(groupNames) == 0 {
			continue
		}
		res := perInstance[groupNames[0]]
		res.GroupKey = gkey

		if res.Changed() {
			if cached, ok := describeCache[res.Tag]; ok {
				res.Manifest = cached.manifest
				if cached.err != nil {
					res.Action, res.Err = ActionFailed, cached.err
				}
			} else if opts.Describe != nil {
				rec, _ := state.GetVersion(key, res.Tag)
				spec := SpecFromRef(g.ref, "", rec, true)
				decl, derr := opts.Describe(context.Background(), spec)
				cb := &described{}
				switch {
				case derr != nil:
					res.Action, res.Err = ActionFailed, fmt.Errorf("plugin %s: installed %s but it could not describe itself: %w", ref.Name, res.Tag, derr)
					cb.err = res.Err
				case checkDeclKind(ref, decl) != nil:
					res.Action, res.Err = ActionFailed, checkDeclKind(ref, decl)
					cb.err = res.Err
				default:
					rec.Manifest = manifestFromDecl(decl)
					state.Put(rec)
					dirty = true
					res.Manifest = rec.Manifest
					cb.manifest = rec.Manifest
				}
				describeCache[res.Tag] = cb
			}
			if res.Action == ActionUpdated {
				opts.logf("plugin %s: updated %s -> %s (sha %s -> %s); permissions: %s",
					ref.Name, "", res.Tag, shortSha(res.PrevSha), shortSha(res.Sha), res.Manifest.Summary())
			} else if res.Action == ActionInstalled {
				opts.logf("plugin %s: installed %s from %s (sha %s); permissions: %s",
					ref.Name, res.Tag, instanceSource(g.ref), shortSha(res.Sha), res.Manifest.Summary())
			}
		}
		out = append(out, res)
	}
	return out, dirty
}

// instanceSource is ref.Source() for whichever instance narrowRef picked as
// the group's representative — used only for a log line.
func instanceSource(ref config.PluginRef) string {
	return ref.Use.Source()
}

// checkDeclKind enforces the kind the reference was declared with against the
// kind the implementation reports. A connector can never be wired as a runtime:
// a runtime EXECUTES agents, so accepting one where a connector was asked for
// would silently escalate what the operator agreed to.
//
// A plugin built against an older SDK reports no kind at all; that is treated as
// "unspecified" and trusted to its block rather than refused, so an additive
// wire change does not break existing plugins.
func checkDeclKind(ref config.PluginRef, decl *Decl) error {
	if decl == nil || decl.Kind == "" {
		return nil
	}
	if string(decl.Kind) == ref.Kind() {
		return nil
	}
	return fmt.Errorf("plugin %s: declared under %s: but it describes itself as a %s — a %s cannot be wired as a %s",
		ref.Name, ref.Use.Kind.Block(), decl.Kind, decl.Kind, ref.Use.Kind)
}

// manifestFromDecl lifts the plugin's declared capabilities into the recorded
// permission manifest.
// SameReloadSurface reports whether a new build's Decl is compatible enough with
// the running one to be hot-swapped IN PLACE (Client.Reload) rather than requiring
// a full daemon restart. The running registrations describe the OLD Decl — its
// kind, ABI, verb/event surface, and permission manifest — so any of those
// changing means a stale registration and forces a restart. A pure
// implementation/bugfix build (same interface) passes and reloads live.
func SameReloadSurface(oldD, newD *Decl) bool {
	if oldD == nil || newD == nil {
		return false
	}
	if oldD.Kind != newD.Kind || oldD.ABI != newD.ABI || oldD.Type != newD.Type {
		return false
	}
	if !sameVerbs(oldD.Verbs, newD.Verbs) || !sameEvents(oldD.Events, newD.Events) {
		return false
	}
	// Permissions must be identical — a widened egress/command/fs set is a new
	// grant the operator must re-consent to at (re)install, never silently live.
	return reflect.DeepEqual(manifestFromDecl(oldD), manifestFromDecl(newD))
}

// sameVerbs / sameEvents compare by name (order-insensitive — a rebuild may emit
// them in a different order) plus full shape (options/outputs schemas).
func sameVerbs(a, b []Verb) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]Verb, len(a))
	for _, v := range a {
		m[v.Name] = v
	}
	for _, v := range b {
		if av, ok := m[v.Name]; !ok || !reflect.DeepEqual(av, v) {
			return false
		}
	}
	return true
}

func sameEvents(a, b []Event) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]Event, len(a))
	for _, e := range a {
		m[e.Name] = e
	}
	for _, e := range b {
		if ae, ok := m[e.Name]; !ok || !reflect.DeepEqual(ae, e) {
			return false
		}
	}
	return true
}

func manifestFromDecl(decl *Decl) Manifest {
	if decl == nil {
		return Manifest{}
	}
	c := decl.Capabilities
	return Manifest{
		Egress:   append([]string(nil), c.Egress...),
		Commands: append([]string(nil), c.Commands...),
		FS:       append([]string(nil), c.FS...),
		Spawns:   c.Spawns || len(c.Commands) > 0,
		Env:      append([]string(nil), c.Env...),
		// Auth (the plugin's declared OAuth2 endpoints) is recorded so
		// `conductor connector auth <name>` can run the interactive login
		// WITHOUT respawning the plugin to re-Describe it.
		Auth: decl.Auth,
		// SingleProcess (the plugin's shared-process requirement) is part of
		// the recorded manifest for the same reason every other capability
		// is: SameReloadSurface compares manifests field for field, so a
		// build that ADDS or DROPS this capability differs from the running
		// one and forces a full restart (never a live in-place swap) — the
		// Manager's shape for this key (one process vs one per instance) is
		// decided once, at construction, and cannot change under a running
		// daemon.
		SingleProcess: c.SingleProcess,
	}
}

func binaryPresent(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func shortSha(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "-"
	}
	return s
}

// PruneOrphans removes installed binaries whose directory no longer corresponds
// to any install-state record — the disk half of `plugin remove`.
func PruneOrphans(state *InstallState) error {
	dir := state.Dir()
	if dir == "" {
		return nil
	}
	keep := map[string]bool{}
	for _, k := range state.Keys() {
		keep[filepath.FromSlash(k)] = true
	}
	for _, kindDir := range []string{"connectors", "runtimes"} {
		ents, err := os.ReadDir(filepath.Join(dir, kindDir))
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			rel := filepath.Join(kindDir, e.Name())
			if keep[rel] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dir, rel)); err != nil {
				return err
			}
		}
	}
	return nil
}
