package store

import (
	"encoding/json"
	"time"
)

// WorkflowRun is the persisted state of an in-flight multi-step workflow so it
// can resume after a conductor restart/crash. Trigger and Action are stored as
// raw JSON so the store stays decoupled from core/config; the engine (re)hydrates
// them. Secrets (tokens) are NOT persisted — they're re-minted on resume.
type WorkflowRun struct {
	ID        string                    `json:"id"`
	Source    string                    `json:"source"`
	Instance  string                    `json:"instance"`
	Kind      string                    `json:"kind"`
	Repo      string                    `json:"repo"`
	Number    int                       `json:"number"`
	Trigger   json.RawMessage           `json:"trigger"` // core.Trigger (tokens stripped)
	Action    json.RawMessage           `json:"action"`  // config.Action (the workflow)
	Outputs   map[string]map[string]any `json:"outputs"` // completed step id -> outputs
	StepIndex int                       `json:"step_index"`
	UpdatedAt time.Time                 `json:"updated_at"`
	// StartHooksFired marks that this run has already passed its
	// workflow-level `start`-phase option/operator hooks (flow.Runner.Run)
	// — set and persisted the first time Run reaches that point, BEFORE any
	// step executes, so a later resume of the SAME run (a crash before step
	// 0 ever checkpointed included) never re-fires them. A retry that
	// deliberately restarts a run from its very first step sets this false
	// on the fresh record it persists (a new attempt, not a continuation);
	// one that continues from a later step sets it true (engine.retryRun).
	// An in-flight run persisted before this field existed decodes it as
	// false — a one-time re-fire on its next resume across the upgrade,
	// never again after.
	StartHooksFired bool `json:"start_hooks_fired,omitempty"`
	// Tokens / CostUSD tally the run's agent spend so far (#36 §14);
	// ApproxCost marks any contributing figure as estimated.
	Tokens     int     `json:"tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	ApproxCost bool    `json:"approx_cost,omitempty"`
}

// PutRun upserts an in-flight workflow run and persists immediately.
func (s *Store) PutRun(r WorkflowRun) error {
	s.mu.Lock()
	r.UpdatedAt = s.now()
	rr := r
	s.runs[r.ID] = &rr
	s.mu.Unlock()
	return s.saveRuns()
}

// DeleteRun removes a finished/failed run.
func (s *Store) DeleteRun(id string) error {
	s.mu.Lock()
	delete(s.runs, id)
	s.mu.Unlock()
	return s.saveRuns()
}

// PendingRuns returns a snapshot of all in-flight runs (for startup resume).
func (s *Store) PendingRuns() []WorkflowRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkflowRun, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, *r)
	}
	return out
}

// saveRuns persists the runs map (best-effort atomic via temp+rename).
func (s *Store) saveRuns() error {
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.runs, "", "  ")
		return b, s.runsPath, err
	})
}
