// Package targets is the process-wide, connector-neutral record of target
// (PR/issue) lifecycle state: which dispatch targets are known merged or
// closed, so a still-running agent's write lands against a live answer
// instead of the stale one it was dispatched against. The engine's `_closed`
// handling records the fact (a trusted target only — see the trigger's
// trusted-target bit; the engine's stopFixers separately stops the PR's
// running fixers). The surfaces that write read it: the gh and git profiles
// (internal/hostcmd, through the jail's TargetClosed) and the github
// connector's scope context (a closed target is no longer the dispatch's
// own). What a write may do is theirs to decide; this package only says
// whether the target is still alive.
package targets

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ttl bounds how long a merged/closed mark is remembered: long enough to catch
// a stray write from an agent whose dispatch limped along after its target
// died, short enough that the map cannot grow without bound across a
// long-lived daemon. Reopen clears a mark immediately regardless of ttl.
const ttl = 7 * 24 * time.Hour

// entry records one target's terminal state.
type entry struct {
	outcome string // "merged" or "closed"
	at      time.Time
}

// Registry is a process-wide target-lifecycle record, shared by every
// dispatch. A target's lifecycle is a daemon-wide fact (not a per-dispatch
// one), which is why Default exists and most callers use it rather than
// building their own.
type Registry struct {
	mu     sync.Mutex
	closed map[string]entry // lower(repo)+"#"+number -> entry
}

// New builds an empty Registry.
func New() *Registry { return &Registry{closed: map[string]entry{}} }

// Default is the registry every dispatch in this process shares.
var Default = New()

// key is the case-insensitive-repo lookup key for repo#number.
func key(repo string, number int) string {
	return strings.ToLower(strings.TrimSpace(repo)) + "#" + fmt.Sprint(number)
}

// MarkClosed records repo#number as terminal — "merged" or "closed"
// (unmerged) — timestamped now. Call this ONLY for a trusted target (the
// platform's own signature-verified fact — the trigger's trusted-target bit): an
// untrusted event choosing its own target must never be able to mark someone
// else's PR closed and have that ride into their write-refusal policy.
func (r *Registry) MarkClosed(repo string, number int, merged bool) {
	outcome := "closed"
	if merged {
		outcome = "merged"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	r.closed[key(repo, number)] = entry{outcome: outcome, at: time.Now()}
}

// Closed reports whether repo#number is a known merged/closed target, and
// which outcome. A mark older than ttl reads as expired (ok=false) even if the
// map entry hasn't been physically pruned yet.
func (r *Registry) Closed(repo string, number int) (outcome string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, found := r.closed[key(repo, number)]
	if !found || time.Since(e.at) > ttl {
		return "", false
	}
	return e.outcome, true
}

// Reopen clears a closed/merged mark: a reopened PR is live again, and its
// writes should no longer be refused as touching a dead target.
func (r *Registry) Reopen(repo string, number int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.closed, key(repo, number))
}

// pruneLocked drops every entry past ttl. Called under mu from a mutating path
// (MarkClosed) so the map cannot grow without bound over a long-lived daemon; a
// read (Closed) checks the deadline itself and needs no prune to be correct.
func (r *Registry) pruneLocked() {
	if len(r.closed) == 0 {
		return
	}
	now := time.Now()
	for k, e := range r.closed {
		if now.Sub(e.at) > ttl {
			delete(r.closed, k)
		}
	}
}
