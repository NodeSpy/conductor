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
	// Agents are the agent sessions this run's steps have running, by the
	// step's run-scoped key, so a restart can resume a step's agent where it
	// was. Owned by PutRunAgent/DeleteRunAgent: PutRun keeps what is stored.
	Agents map[string]RunAgent `json:"agents,omitempty"`
	// InfraWait marks a run that is WAITING for its agent runtime: a step
	// failed for an infrastructure reason (the runtime could not reach its
	// model, returned nothing, did not start), so the run was kept at that
	// step instead of failing. The engine resumes it once the runtime works
	// again, or drops it when its target is gone. nil: not waiting.
	InfraWait *InfraWait `json:"infra_wait,omitempty"`
	// InfraResumes counts the times this run was resumed after an
	// infrastructure wait; the flow bounds it, so a failure that only looks
	// like one cannot cycle forever.
	InfraResumes int `json:"infra_resumes,omitempty"`
}

// InfraWait is why and since when a run waits for its agent runtime.
type InfraWait struct {
	Since  time.Time `json:"since"`
	Reason string    `json:"reason"`
	// Step is the step the run resumes from.
	Step string `json:"step,omitempty"`
	// Runtime / Model / Provider are what the failed step ran on: the
	// recovery probes exactly that.
	Runtime  string `json:"runtime,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Subject is what failed when it was not an agent runtime ("plugin
	// <name>": a call it did not answer). Such a wait has no probe: the
	// step itself is re-run on the backoff.
	Subject string `json:"subject,omitempty"`
	// History is the execution record the wait ended; the resumed execution
	// links back to it.
	History string `json:"history,omitempty"`
	// Head is the target's revision when the wait began: a target that has
	// moved on since is a superseded run, dropped instead of resumed.
	Head string `json:"head,omitempty"`
	// Escalated: the outage this run waits out was already reported, so a
	// restart does not report it again.
	Escalated bool `json:"escalated,omitempty"`
}

// RunAgent is one step's running agent: the session its runtime reported and
// the workspace it works in.
type RunAgent struct {
	Runtime   string `json:"runtime,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	CanResume bool   `json:"can_resume,omitempty"`
	// DispatchID is the dispatch the agent ran under; its resume keeps it.
	DispatchID string `json:"dispatch_id,omitempty"`
	// Resumes counts the restarts this step's agent has been resumed through
	// without finishing.
	Resumes int       `json:"resumes,omitempty"`
	Started time.Time `json:"started"`
}

// PutRun upserts an in-flight workflow run and persists immediately.
func (s *Store) PutRun(r WorkflowRun) error {
	s.mu.Lock()
	r.UpdatedAt = s.now()
	rr := r
	rr.Agents = nil
	if cur := s.runs[r.ID]; cur != nil {
		rr.Agents = cur.Agents
	}
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

// RunAgent returns the agent recorded for one step of a run.
func (s *Store) RunAgent(runID, key string) (RunAgent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[runID]
	if r == nil {
		return RunAgent{}, false
	}
	a, ok := r.Agents[key]
	return a, ok
}

// PutRunAgent records the agent one step of a run has running. A run that is
// not stored (finished, or never persisted) records nothing.
func (s *Store) PutRunAgent(runID, key string, a RunAgent) error {
	s.mu.Lock()
	r := s.runs[runID]
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	agents := make(map[string]RunAgent, len(r.Agents)+1)
	for k, v := range r.Agents {
		agents[k] = v
	}
	agents[key] = a
	r.Agents = agents
	s.mu.Unlock()
	return s.saveRuns()
}

// DeleteRunAgent drops a step's agent record: the step finished.
func (s *Store) DeleteRunAgent(runID, key string) error {
	s.mu.Lock()
	r := s.runs[runID]
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	if _, ok := r.Agents[key]; !ok {
		s.mu.Unlock()
		return nil
	}
	agents := make(map[string]RunAgent, len(r.Agents))
	for k, v := range r.Agents {
		if k != key {
			agents[k] = v
		}
	}
	if len(agents) == 0 {
		agents = nil
	}
	r.Agents = agents
	s.mu.Unlock()
	return s.saveRuns()
}

// KeepsWorkspace reports whether a stored run still has an agent working in
// dir, so a restart can resume it there: its workspace must not be reclaimed.
func (s *Store) KeepsWorkspace(dir string) bool {
	if dir == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		for _, a := range r.Agents {
			if a.Workspace == dir || a.Cwd == dir {
				return true
			}
		}
	}
	return false
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
