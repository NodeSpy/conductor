package store

import (
	"encoding/json"
	"os"
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
	s.mu.Lock()
	b, err := json.MarshalIndent(s.runs, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.runsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.runsPath)
}
