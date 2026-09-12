package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	Key      string // "<kind-dir>/<name>"
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

// Reconcile brings install state in line with the config's referenced plugins.
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

	var (
		results []Resolution
		dirty   bool
	)
	for _, key := range keys {
		ref := refs[key]
		if opts.Only != "" && opts.Only != ref.Name && opts.Only != key {
			continue
		}
		res, changed := reconcileOne(key, ref, state, trust, api, opts)
		dirty = dirty || changed
		results = append(results, res)
	}

	// Drop records for plugins the config no longer references, so install
	// state does not accumulate forever. The BINARY is left on disk: removing
	// it is `plugin remove`'s job, and a reference removed by mistake should be
	// cheap to restore.
	if opts.Only == "" && !opts.GapsOnly {
		for _, k := range state.Keys() {
			if _, still := refs[k]; !still {
				state.Delete(k)
				dirty = true
				opts.logf("plugin %s: no longer referenced by the config — dropped from install state", k)
			}
		}
	}
	if dirty {
		if err := state.Save(); err != nil {
			return results, err
		}
	}
	return results, nil
}

func reconcileOne(key string, ref config.PluginRef, state *InstallState, trust *config.PackTrustConfig, api ReleaseAPI, opts Options) (Resolution, bool) {
	res := Resolution{
		Key: key, Name: ref.Name, Kind: ref.Kind(),
		Use: ref.Use.String(), Origin: string(ref.Use.Origin),
	}
	prev, installed := state.Get(key)

	// A local development binary is not fetched, verified against a release, or
	// pinned — it is whatever the operator built. Record it so `plugin list`
	// can show it, and move on.
	if ref.Use.Origin == config.OriginLocal {
		res.Action, res.Path = ActionLocal, ref.Use.Path
		return res, false
	}

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

	rs := RemoteSource{Repo: ref.Use.Repo, Component: ref.Use.Component}
	dir := BinDirFor(state.Dir(), key)
	binPath, tag, sha, err := FetchRemote(rs, ref.Use.Version, "", dir, api)
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
		// constraint); keep the record honest.
		if prev.Use != ref.Use.String() {
			prev.Use = ref.Use.String()
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
		Resolved: tag, Sha256: sha, Path: binPath,
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
