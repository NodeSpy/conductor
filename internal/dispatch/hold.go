package dispatch

import "sync"

// HoldSet is the set of agent ids the conductor has handed off for you to drive
// interactively (a background workflow step). The reaper skips anything in it
// unconditionally — a hand-off sits idle *because* it's waiting for you, and it
// carries no reaper-observable "needs you" signal (no pending permission or hold
// marker), so relying on the absence of the archive=1 label was too fragile: a
// label leak or a shared-workspace cull could still take it down. This is the
// positive, deterministic keep-signal.
//
// It is in-memory (not persisted): a hand-off launched before a restart isn't
// re-held, but such an agent also never carried archive=1, so the reaper's label
// filter already leaves it be. New hand-offs launched after start are held here.
type HoldSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func NewHoldSet() *HoldSet { return &HoldSet{m: map[string]bool{}} }

// Add marks an agent id as held (a no-op for the empty id or a nil set).
func (h *HoldSet) Add(id string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	h.m[id] = true
	h.mu.Unlock()
}

// Has reports whether an agent id is held.
func (h *HoldSet) Has(id string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.m[id]
}

// keepOnly drops held ids not in present, so the set stays bounded once you
// archive a hand-off yourself. A nil present means the caller couldn't list agents
// (e.g. a transient error) — skip pruning rather than wrongly forget every hold.
func (h *HoldSet) keepOnly(present map[string]bool) {
	if h == nil || present == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.m {
		if !present[id] {
			delete(h.m, id)
		}
	}
}
