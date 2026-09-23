package dispatch

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

// OwnedSet is conductor's authoritative record of the paseo agents and
// workspaces IT launched. Archival is gated on this set — nothing conductor did
// not create is ever eligible, no matter its isolation type, name, or state.
//
// This exists because inferring ownership from heuristics is unsafe: a sweep
// keyed on "any worktree" once archived a pile of the USER's own paseo
// worktrees (and paseo's archive force-removed their directories). Ownership is
// now a positive fact recorded at the moment conductor creates the workspace /
// launches the agent — never guessed, and never scanned for: there is no
// background sweep. An owned agent is archived when its step completes
// (foreground), when it calls step.done / handoff.done, or when its failed
// launch is torn down — each acting on one specific recorded id.
//
// It persists to a small JSON file so ownership survives a daemon restart: a
// hand-off's done call can arrive after a deploy, and the ledger must still
// know that agent was conductor's.
//
// Dispatches maps conductor's dispatch id to the paseo agent id it launched.
// It is how a `done` call is resolved: the caller's session token carries the
// dispatch id the daemon minted at launch (never an agent-supplied id), and
// this binding turns it into the one agent that dispatch created.
type OwnedSet struct {
	mu         sync.Mutex
	workspaces map[string]bool
	agents     map[string]bool
	dispatches map[string]string // dispatch id -> agent id
	path       string            // "" = in-memory only (tests)
}

// NewOwnedSet builds an ownership ledger, loading any persisted ids from path.
// An empty path keeps it in-memory (tests).
func NewOwnedSet(path string) *OwnedSet {
	o := &OwnedSet{
		workspaces: map[string]bool{},
		agents:     map[string]bool{},
		dispatches: map[string]string{},
		path:       path,
	}
	o.load()
	return o
}

// AddWorkspace records a workspace id conductor created (no-op for nil/empty).
func (o *OwnedSet) AddWorkspace(id string) {
	if o == nil {
		return
	}
	o.add(&o.workspaces, id)
}

// AddAgent records an agent id conductor launched (no-op for nil/empty).
func (o *OwnedSet) AddAgent(id string) {
	if o == nil {
		return
	}
	o.add(&o.agents, id)
}

func (o *OwnedSet) add(set *map[string]bool, id string) {
	if id == "" {
		return
	}
	o.mu.Lock()
	if !(*set)[id] {
		(*set)[id] = true
		o.save()
	}
	o.mu.Unlock()
}

// BindDispatch records which agent a dispatch launched, so a done call bearing
// that dispatch id (from its session token) resolves to exactly that agent.
func (o *OwnedSet) BindDispatch(dispatchID, agentID string) {
	if o == nil || dispatchID == "" || agentID == "" {
		return
	}
	o.mu.Lock()
	if o.dispatches[dispatchID] != agentID {
		o.dispatches[dispatchID] = agentID
		o.save()
	}
	o.mu.Unlock()
}

// AgentForDispatch resolves a dispatch id to the agent it launched ("" when
// unknown — the dispatch never launched, or was already archived+forgotten).
func (o *OwnedSet) AgentForDispatch(dispatchID string) string {
	if o == nil || dispatchID == "" {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dispatches[dispatchID]
}

// HasWorkspace reports whether conductor created this workspace.
func (o *OwnedSet) HasWorkspace(id string) bool {
	if o == nil {
		return false
	}
	return o.has(o.workspaces, id)
}

// HasAgent reports whether conductor launched this agent.
func (o *OwnedSet) HasAgent(id string) bool {
	if o == nil {
		return false
	}
	return o.has(o.agents, id)
}

func (o *OwnedSet) has(set map[string]bool, id string) bool {
	if o == nil || id == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return set[id]
}

// forget drops ids once they are archived/gone, keeping the ledger bounded.
// Dispatch bindings pointing at the forgotten agent are dropped with it.
// Absent ids are ignored.
func (o *OwnedSet) forget(workspaceID, agentID string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	changed := false
	if workspaceID != "" && o.workspaces[workspaceID] {
		delete(o.workspaces, workspaceID)
		changed = true
	}
	if agentID != "" && o.agents[agentID] {
		delete(o.agents, agentID)
		changed = true
	}
	if agentID != "" {
		for d, a := range o.dispatches {
			if a == agentID {
				delete(o.dispatches, d)
				changed = true
			}
		}
	}
	if changed {
		o.save()
	}
	o.mu.Unlock()
}

func (o *OwnedSet) load() {
	if o.path == "" {
		return
	}
	b, err := os.ReadFile(o.path)
	if err != nil {
		return
	}
	var rec struct {
		Workspaces []string          `json:"workspaces"`
		Agents     []string          `json:"agents"`
		Dispatches map[string]string `json:"dispatches"`
	}
	if json.Unmarshal(b, &rec) != nil {
		return
	}
	for _, id := range rec.Workspaces {
		if id != "" {
			o.workspaces[id] = true
		}
	}
	for _, id := range rec.Agents {
		if id != "" {
			o.agents[id] = true
		}
	}
	for d, a := range rec.Dispatches {
		if d != "" && a != "" {
			o.dispatches[d] = a
		}
	}
}

// save persists the ledger (best-effort, atomic). Caller holds o.mu.
func (o *OwnedSet) save() {
	if o.path == "" {
		return
	}
	rec := struct {
		Workspaces []string          `json:"workspaces"`
		Agents     []string          `json:"agents"`
		Dispatches map[string]string `json:"dispatches"`
	}{
		Workspaces: keys(o.workspaces),
		Agents:     keys(o.agents),
		Dispatches: o.dispatches,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	tmp := o.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, o.path)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
