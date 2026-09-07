package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The outcome-learning loop's persistence (#36 §18). Two small files beside
// the state file:
//
//   - engagements.json — which agents acted on which PR/issue (with their
//     run/workflow attribution and spend), waiting for the target's terminal
//     signal (merged / closed / reverted). Bounded per target and pruned by
//     age; a terminal outcome consumes the target's engagements.
//   - outcome_stats.json — per-agent outcome counters (merged / closed /
//     reverted / approved / rejected / ci_failed), the durable feed behind
//     optional per-profile guidance tuning. The audit's `outcome` rows are
//     the full record; these counters are the cheap always-loaded summary.

// Engagement is one agent's recorded work on a target.
type Engagement struct {
	Agent string `json:"agent"`
	// Workflow is the trigger scope ("on[/name]"); SavedWorkflow the promoted
	// workflow (#36 §11) the step ran inside, when it did — the outcome feeds
	// that workflow's delivery health.
	Workflow      string    `json:"workflow,omitempty"`
	SavedWorkflow string    `json:"saved_workflow,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	Run           string    `json:"run,omitempty"` // history id
	CostUSD       float64   `json:"cost_usd,omitempty"`
	Tokens        int       `json:"tokens,omitempty"`
	At            time.Time `json:"at"`
}

const (
	// engagementMaxAge prunes engagements whose target never resolved.
	engagementMaxAge = 30 * 24 * time.Hour
	// engagementCap bounds one target's engagement list (repeated agent
	// touches on a long-lived PR keep the newest).
	engagementCap = 20
)

func targetKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

// RecordEngagement notes that an agent acted on a target.
func (s *Store) RecordEngagement(repo string, number int, e Engagement) {
	if repo == "" || number <= 0 || e.Agent == "" {
		return
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	s.mu.Lock()
	if s.engagements == nil {
		s.engagements = map[string][]Engagement{}
	}
	key := targetKey(repo, number)
	list := append(s.engagements[key], e)
	if len(list) > engagementCap {
		list = list[len(list)-engagementCap:]
	}
	s.engagements[key] = list
	s.pruneEngagementsLocked()
	s.mu.Unlock()
	s.saveEngagements()
}

// TakeEngagements returns and CLEARS a target's engagements (a terminal
// outcome — merged/closed/reverted — consumes them).
func (s *Store) TakeEngagements(repo string, number int) []Engagement {
	s.mu.Lock()
	key := targetKey(repo, number)
	out := s.engagements[key]
	delete(s.engagements, key)
	s.mu.Unlock()
	if len(out) > 0 {
		s.saveEngagements()
	}
	return out
}

// PeekEngagements returns a target's engagements without consuming them
// (non-terminal signals: a CI failure on a still-open PR).
func (s *Store) PeekEngagements(repo string, number int) []Engagement {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Engagement(nil), s.engagements[targetKey(repo, number)]...)
}

// pruneEngagementsLocked drops entries older than the retention window.
func (s *Store) pruneEngagementsLocked() {
	cut := s.now().Add(-engagementMaxAge)
	for key, list := range s.engagements {
		kept := list[:0]
		for _, e := range list {
			if e.At.After(cut) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(s.engagements, key)
		} else {
			s.engagements[key] = kept
		}
	}
}

// BumpOutcome increments one agent's outcome counter.
func (s *Store) BumpOutcome(agent, outcome string) {
	if agent == "" || outcome == "" {
		return
	}
	s.mu.Lock()
	if s.outcomeStats == nil {
		s.outcomeStats = map[string]map[string]int{}
	}
	if s.outcomeStats[agent] == nil {
		s.outcomeStats[agent] = map[string]int{}
	}
	s.outcomeStats[agent][outcome]++
	s.mu.Unlock()
	s.saveOutcomeStats()
}

// AgentOutcomeStats returns a copy of one agent's outcome counters.
func (s *Store) AgentOutcomeStats(agent string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.outcomeStats[agent] {
		out[k] = v
	}
	return out
}

func (s *Store) engagementsPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "engagements.json")
}

func (s *Store) outcomeStatsPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "outcome_stats.json")
}

func (s *Store) saveEngagements() {
	path := s.engagementsPath()
	if path == "" {
		return
	}
	s.mu.Lock()
	b, err := json.MarshalIndent(s.engagements, "", " ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (s *Store) saveOutcomeStats() {
	path := s.outcomeStatsPath()
	if path == "" {
		return
	}
	s.mu.Lock()
	b, err := json.MarshalIndent(s.outcomeStats, "", " ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// loadOutcomeState loads both files at Open (missing/corrupt = start fresh).
func (s *Store) loadOutcomeState() {
	if p := s.engagementsPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &s.engagements)
		}
	}
	if s.engagements == nil {
		s.engagements = map[string][]Engagement{}
	}
	if p := s.outcomeStatsPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &s.outcomeStats)
		}
	}
	if s.outcomeStats == nil {
		s.outcomeStats = map[string]map[string]int{}
	}
}
