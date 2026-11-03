package agentsandbox

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Ephemeral homes. An identity home that outlives its dispatch is a place
// one run plants what the next run of the identity executes: a settings
// hook, a shell rc file, a poisoned build cache. By default
// (sandbox.persist: none) a dispatch's agent gets a home of its own — fresh,
// seeded from the operator's settings like any new home, kept for the
// dispatch's resumes and removed a day after its session ends. `cache` adds
// back a per-identity cache directory (~/.cache, and ~/Library/Caches on
// macOS); `home` keeps the whole identity home across dispatches, as before.
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

// DispatchHomeTTL is how long a per-dispatch home outlives its session.
var DispatchHomeTTL = 24 * time.Hour

// homeFor is where e's home lives: the identity's own, or one of the
// dispatch's.
func (r *Resolver) homeFor(e Entry) (root, key string) {
	if r.Persist == PersistHome || e.Dispatch == "" {
		return r.StateRoot, e.Identity
	}
	return filepath.Join(r.StateRoot, dispatchHomes), e.Identity + "@" + e.Dispatch
}

// persistCache links the identity's persisted cache into a per-dispatch
// home (persist: cache) and returns the directory the launch must see
// read-write ("" = none).
func (r *Resolver) persistCache(e Entry, home string) (string, error) {
	if r.Persist != PersistCache || e.Dispatch == "" {
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

var lastSweep atomic.Int64

// maybeSweep runs SweepDispatchHomes at most hourly, in the background.
func (r *Resolver) maybeSweep() {
	now := time.Now().Unix()
	if last := lastSweep.Load(); now-last < 3600 || !lastSweep.CompareAndSwap(last, now) {
		return
	}
	go r.SweepDispatchHomes()
}

// SweepDispatchHomes removes the per-dispatch homes whose session has
// ended (no registered workspace of their dispatch) and that nothing has
// touched for DispatchHomeTTL.
func (r *Resolver) SweepDispatchHomes() int {
	root := filepath.Join(r.StateRoot, dispatchHomes)
	ents, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	live := map[string]bool{}
	for _, e := range r.registry().entries() {
		if e.Dispatch != "" {
			live[IdentityKey(e.Identity+"@"+e.Dispatch)] = true
		}
	}
	n := 0
	for _, ent := range ents {
		if live[ent.Name()] {
			continue
		}
		p := filepath.Join(root, ent.Name())
		fi, err := os.Lstat(p)
		if err != nil || time.Since(fi.ModTime()) < DispatchHomeTTL {
			continue
		}
		if os.RemoveAll(p) == nil {
			n++
		}
	}
	return n
}
