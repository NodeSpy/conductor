package dispatch

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

// HoldSet is the set of agent ids the conductor has handed off for you to drive
// interactively (a background workflow step). The reaper skips anything in it
// unconditionally — a hand-off sits idle *because* it's waiting for you, and it
// carries no reaper-observable "needs you" signal (no pending permission or hold
// marker), so relying on the absence of the archive=1 label was too fragile: a
// label leak or a shared-workspace cull could still take it down. This is the
// positive, deterministic keep-signal.
//
// It persists to a small JSON file (when a path is given) so a hand-off launched
// before a restart keeps its protection across a deploy — otherwise the reaper,
// with an empty in-memory set, could archive it once past the startup grace. The
// set is pruned as agents disappear (you archive them yourself).
type HoldSet struct {
	mu   sync.Mutex
	m    map[string]bool
	path string // "" = in-memory only (tests)
}

// NewHoldSet builds a hold set, loading any persisted ids from path. An empty path
// keeps it in-memory (no persistence).
func NewHoldSet(path string) *HoldSet {
	h := &HoldSet{m: map[string]bool{}, path: path}
	h.load()
	return h
}

// Add marks an agent id as held (a no-op for the empty id or a nil set).
func (h *HoldSet) Add(id string) {
	if h == nil || id == "" {
		return
	}
	h.mu.Lock()
	if !h.m[id] {
		h.m[id] = true
		h.save()
	}
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
	changed := false
	for id := range h.m {
		if !present[id] {
			delete(h.m, id)
			changed = true
		}
	}
	if changed {
		h.save()
	}
}

// load reads persisted ids (best-effort — a missing/corrupt file yields an empty
// set). Called once at construction, so no lock is needed.
func (h *HoldSet) load() {
	if h.path == "" {
		return
	}
	b, err := os.ReadFile(h.path)
	if err != nil {
		return
	}
	var ids []string
	if json.Unmarshal(b, &ids) != nil {
		return
	}
	for _, id := range ids {
		if id != "" {
			h.m[id] = true
		}
	}
}

// save persists the current ids (best-effort). Caller holds h.mu.
func (h *HoldSet) save() {
	if h.path == "" {
		return
	}
	ids := make([]string, 0, len(h.m))
	for id := range h.m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.Marshal(ids)
	if err != nil {
		return
	}
	tmp := h.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, h.path)
	}
}
