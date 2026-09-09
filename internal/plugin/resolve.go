package plugin

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
)

// Remote-plugin resolution (#59). ResolvePlugins turns each REMOTE `plugins:`
// entry (github.com/owner/repo//component) into a fetched, checksum-verified,
// vendored binary and records it in the lockfile, so a subsequent offline boot
// runs it exactly like a local sha-pinned plugin. It is the plugin analogue of
// config.ResolvePacks: called by `conductor init` and the auto-update loop.

// Resolution is one plugin's resolve outcome, for logging + the CLI preview.
type Resolution struct {
	Name   string // plugins: map key
	Source string // remote source as written
	Action string // "fetched" | "held" | "up-to-date" | "skipped-local" | "error"
	Tag    string // resolved release tag (fetched/held)
	Sha    string // verified binary sha (fetched/held)
	Path   string // vendored binary path, relative to configDir
	Err    error  // set when Action == "error"
}

// ResolvePlugins resolves every remote plugin in `plugins` and merges the
// results into the lockfile under configDir. Local-path plugins are reported
// skipped and left untouched (they carry their own sha256: pin). A held remote
// plugin keeps its existing lock entry (no re-fetch). The `trust` allowlist
// gates remote sources unless allowUnlisted. `api` is the release transport
// (GHReleaseAPI in production; a stub in tests).
func ResolvePlugins(configDir string, plugins map[string]config.PluginRef, trust *config.PackTrustConfig, allowUnlisted bool, api ReleaseAPI) ([]Resolution, error) {
	names := make([]string, 0, len(plugins))
	for n := range plugins {
		names = append(names, n)
	}
	sort.Strings(names)

	var (
		results []Resolution
		locks   []config.PluginLockEntry
	)
	for _, name := range names {
		ref := plugins[name]
		if !ref.IsRemote() {
			results = append(results, Resolution{Name: name, Source: ref.Source, Action: "skipped-local"})
			continue
		}
		res := Resolution{Name: name, Source: ref.Source}

		if !allowUnlisted && !trust.SourceAllowed(ref.Source) {
			res.Action, res.Err = "error", fmt.Errorf("plugin %q: source %q is not in plugin_trust.allow — add it to the allowlist or re-run with --allow-unlisted", name, ref.Source)
			results = append(results, res)
			return results, res.Err
		}

		// A held plugin keeps whatever it is currently locked at.
		if ref.Hold {
			if prev, ok := config.PluginLock(configDir, name); ok {
				res.Action, res.Tag, res.Sha, res.Path = "held", prev.Resolved, prev.Sha256, prev.Path
				locks = append(locks, prev)
				results = append(results, res)
				continue
			}
			// Held but never resolved — fall through and fetch once so there is a
			// pin to hold; a hold can't freeze nothing.
		}

		rs, ok := ParseRemoteSource(ref.Source)
		if !ok {
			res.Action, res.Err = "error", fmt.Errorf("plugin %q: unrecognized remote source %q", name, ref.Source)
			results = append(results, res)
			return results, res.Err
		}
		vendor := filepath.Join(config.PluginVendorDir(configDir), name)
		binPath, tag, sha, err := FetchRemote(rs, ref.Version, ref.Sha256, vendor, api)
		if err != nil {
			res.Action, res.Err = "error", fmt.Errorf("plugin %q: %w", name, err)
			results = append(results, res)
			return results, res.Err
		}
		rel, rerr := filepath.Rel(configDir, binPath)
		if rerr != nil {
			rel = binPath // absolute fallback; Load handles both
		}
		entry := config.PluginLockEntry{Name: name, Source: ref.Source, Version: ref.Version, Resolved: tag, Sha256: sha, Path: rel}
		locks = append(locks, entry)

		res.Action, res.Tag, res.Sha, res.Path = "fetched", tag, sha, rel
		if prev, ok := config.PluginLock(configDir, name); ok && prev.Sha256 == sha && prev.Resolved == tag {
			res.Action = "up-to-date"
		}
		results = append(results, res)
	}

	if len(locks) > 0 {
		if err := config.MergePluginLock(configDir, locks); err != nil {
			return results, fmt.Errorf("write plugin lock: %w", err)
		}
	}
	return results, nil
}
