package agentsandbox

import (
	"os"
	"path/filepath"
)

// Ephemeral homes. An identity home that outlives its dispatch is a place
// one run plants what the next run of the identity executes: a settings
// hook, a shell rc file, a poisoned build cache. By default
// (sandbox.persist: none) a dispatch's agent gets a home of its own — fresh,
// seeded from the operator's settings like any new home, kept while its run
// or an agent in it can still use it and removed when neither can
// (reclaim.go). `cache` adds back a per-identity cache directory (~/.cache,
// and ~/Library/Caches on macOS); `home` keeps the whole identity home
// across dispatches, as before.
// A launch with no dispatch (a bot's home session) keeps its identity's
// home either way.

// Persist modes (sandbox.persist).
const (
	PersistNone  = "none"
	PersistCache = "cache"
	PersistHome  = "home"
)

// dispatchHomes holds the per-dispatch homes, a dot name no identity's
// home can take (IdentityKey trims leading dots).
const dispatchHomes = ".dispatch-homes"

// cacheRoots holds each identity's persisted cache (persist: cache).
const cacheRoots = ".caches"

// homeFor is where e's home lives under persist (sandbox.persist, or its
// scope's): the identity's own, or one of the dispatch's.
func (r *Resolver) homeFor(e Entry, persist string) (root, key string) {
	if persist == PersistHome || e.Dispatch == "" {
		return r.StateRoot, e.Identity
	}
	return filepath.Join(r.StateRoot, dispatchHomes), dispatchHomeKey(e)
}

// dispatchHomeKey names e's per-dispatch home: its identity at the dispatch
// the home was created for (Entry.Home, a resumed step's), or its own.
func dispatchHomeKey(e Entry) string {
	d := e.Dispatch
	if e.Home != "" {
		d = e.Home
	}
	return e.Identity + "@" + d
}

// persistCache links the identity's persisted cache into a per-dispatch
// home (persist: cache) and returns the directory the launch must see
// read-write ("" = none).
func (r *Resolver) persistCache(e Entry, home, persist string) (string, error) {
	if persist != PersistCache || e.Dispatch == "" {
		return "", nil
	}
	dir := filepath.Join(r.StateRoot, cacheRoots, IdentityKey(e.Identity))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	links := []string{".cache"}
	if r.GOOS == "darwin" {
		links = append(links, filepath.Join("Library", "Caches"))
	}
	for _, rel := range links {
		target := filepath.Join(dir, filepath.Base(rel))
		if err := os.MkdirAll(target, 0o700); err != nil {
			return "", err
		}
		p := filepath.Join(home, rel)
		if err := homeDir(home, filepath.Dir(p)); err != nil {
			return "", err
		}
		if cur, err := os.Readlink(p); err == nil && cur == target {
			continue
		}
		_ = os.RemoveAll(p)
		if err := os.Symlink(target, p); err != nil {
			return "", err
		}
	}
	return dir, nil
}
