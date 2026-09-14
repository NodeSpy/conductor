package store

import (
	"encoding/json"
)

// PlanRecord is one in-flight agent plan's checkpoint (#36 §11): the plan's
// steps (as emitted + revised), the resume index, the committed steps'
// outputs, and the revision count — keyed by the owning workflow run and the
// emitting agent step. Persisted beside runs.json so a daemon crash or
// auto-update restart resumes the plan AFTER its last committed step instead
// of re-running committed side effects. Removed on completion and on
// terminal failure (a failed plan compensated; a restart must not revive it).
type PlanRecord struct {
	RunID     string                    `json:"run_id"`
	StepID    string                    `json:"step_id"`
	Agent     string                    `json:"agent"`
	Steps     []byte                    `json:"steps"` // YAML: the plan grammar is yaml-tagged
	Next      int                       `json:"next"`
	Revisions int                       `json:"revisions"`
	Outputs   map[string]map[string]any `json:"outputs,omitempty"` // committed step outputs by id
}

func planKey(runID, stepID string) string { return runID + "\x00" + stepID }

// PutPlan upserts a plan checkpoint and persists immediately.
func (s *Store) PutPlan(rec PlanRecord) error {
	s.mu.Lock()
	cp := rec
	s.plans[planKey(rec.RunID, rec.StepID)] = &cp
	s.mu.Unlock()
	return s.savePlans()
}

// GetPlan returns the checkpoint for one run's agent step, if any.
func (s *Store) GetPlan(runID, stepID string) (PlanRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.plans[planKey(runID, stepID)]
	if !ok {
		return PlanRecord{}, false
	}
	// Copy the Outputs map, not just the struct. A shallow copy hands the
	// caller the SAME map the store keeps, so a caller mutating its own
	// result silently rewrites persisted state — and the next save writes
	// the mutation out as if it had been recorded.
	out := *rec
	if rec.Outputs != nil {
		out.Outputs = make(map[string]map[string]any, len(rec.Outputs))
		for k, v := range rec.Outputs {
			inner := make(map[string]any, len(v))
			for ik, iv := range v {
				inner[ik] = iv
			}
			out.Outputs[k] = inner
		}
	}
	return out, true
}

// DeletePlan removes a finished (or terminally failed) plan's checkpoint.
func (s *Store) DeletePlan(runID, stepID string) error {
	s.mu.Lock()
	_, existed := s.plans[planKey(runID, stepID)]
	delete(s.plans, planKey(runID, stepID))
	s.mu.Unlock()
	if !existed {
		return nil
	}
	return s.savePlans()
}

// savePlans persists the checkpoint map (atomic temp+rename; 0600 — plan
// outputs are workflow data).
func (s *Store) savePlans() error {
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.plans, "", "  ")
		return b, s.plansPath, err
	})
}
