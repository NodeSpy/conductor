package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The outcome-learning loop's persistence (#36 §18). Three small files beside
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
//   - ci_failed.json — the head SHA that last produced a `ci_failed` outcome
//     per target, so a fail-fast matrix's fan-out of cancelled-check triggers
//     records one ci_failed per push, not one per check event. Pruned by age
//     like engagements; a terminal outcome clears it with them.

// Engagement is one step's recorded work on a target.
//
// Key is the outcome TRACK-RECORD KEY: the step identity by default, or the
// step's explicit outcome_key (docs/design/agents-removal.md §4). Its JSON
// tag stays "agent" — the field it replaced — so engagements recorded before
// the removal keep resolving, and a migrated config (whose steps carry the
// old agent name as their `name:`) matches its accumulated history exactly.
type Engagement struct {
	Key string `json:"agent"`
	// Runtime is the backend the work executed on — the budget anchor (§1),
	// carried so the report can attribute spend per runtime.
	Runtime string `json:"runtime,omitempty"`
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

// ciFailMark remembers the head SHA that last produced a `ci_failed` outcome for
// a target. A fail-fast CI matrix emits one failing_checks trigger per cancelled
// sibling check — dozens for a single failed push — so without this the loop
// would record dozens of identical ci_failed rows. Pruned by age like the
// engagements it gates.
type ciFailMark struct {
	Head string    `json:"head"`
	At   time.Time `json:"at"`
}

// targetKey is retained for the revert path, which addresses a repo#number
// the CALLER already vetted (a corroborated revert names sibling PRs in the
// same trusted repo). Everything driven by an incoming trigger passes
// core.Trigger.Key() instead, which namespaces an untrusted target away from
// a trusted one — see the key parameters below.
func targetKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

// TargetKey is the engagement key for a repo#number the caller has already
// vetted — the trusted-repo spelling core.Trigger.Key produces for a
// platform-assigned target. The revert path uses it to address a SIBLING PR
// of the trusted repo it is already inside.
func TargetKey(repo string, number int) string { return targetKey(repo, number) }

// RecordEngagement notes that a step acted on a target.
func (s *Store) RecordEngagement(key string, e Engagement) {
	if key == "" || e.Key == "" {
		return
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	s.mu.Lock()
	if s.engagements == nil {
		s.engagements = map[string][]Engagement{}
	}

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
// outcome — merged/closed/reverted — consumes them). The target's ci_failed
// marker is cleared with them.
func (s *Store) TakeEngagements(key string) []Engagement {
	s.mu.Lock()

	out := s.engagements[key]
	delete(s.engagements, key)
	_, hadMark := s.ciFailed[key]
	delete(s.ciFailed, key)
	s.mu.Unlock()
	if len(out) > 0 {
		s.saveEngagements()
	}
	if hadMark {
		s.saveCIFailed()
	}
	return out
}

// PeekEngagements returns a target's engagements without consuming them
// (non-terminal signals: a CI failure on a still-open PR).
func (s *Store) PeekEngagements(key string) []Engagement {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Engagement(nil), s.engagements[key]...)
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

// MarkCIFailure records a CI failure of (repo, number) at head, reporting whether
// this is the FIRST failing_checks seen for that head. The caller records a
// `ci_failed` outcome only when it returns true, so a fail-fast matrix (one job
// fails, its siblings cancel) collapses to one ci_failed per push instead of one
// per check event; a later push that fails again is a new head and records anew.
// An empty head is never deduped (fail-safe: record rather than drop the signal).
func (s *Store) MarkCIFailure(key, head string) bool {
	if key == "" || head == "" {
		return true
	}

	s.mu.Lock()
	if s.ciFailed == nil {
		s.ciFailed = map[string]ciFailMark{}
	}
	if s.ciFailed[key].Head == head {
		s.mu.Unlock()
		return false
	}
	s.ciFailed[key] = ciFailMark{Head: head, At: s.now()}
	s.pruneCIFailedLocked()
	s.mu.Unlock()
	s.saveCIFailed()
	return true
}

// pruneCIFailedLocked drops markers past the engagement retention window (they
// share a lifecycle with the engagements they gate). Called on the write path so
// the map stays bounded even on repos conductor never dispatches into.
func (s *Store) pruneCIFailedLocked() {
	cut := s.now().Add(-engagementMaxAge)
	for key, m := range s.ciFailed {
		if !m.At.After(cut) {
			delete(s.ciFailed, key)
		}
	}
}

// BumpOutcome increments one track-record key's outcome counter. The key is
// a step identity (or an explicit outcome_key) — see Engagement.
func (s *Store) BumpOutcome(key, outcome string) {
	if key == "" || outcome == "" {
		return
	}
	s.mu.Lock()
	if s.outcomeStats == nil {
		s.outcomeStats = map[string]map[string]int{}
	}
	if s.outcomeStats[key] == nil {
		s.outcomeStats[key] = map[string]int{}
	}
	s.outcomeStats[key][outcome]++
	s.mu.Unlock()
	s.saveOutcomeStats()
}

// OutcomeStats returns a copy of one track-record key's outcome counters.
func (s *Store) OutcomeStats(key string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.outcomeStats[key] {
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
	// Through persist: it holds writeMu across marshal→write→rename,
	// so a concurrent save of a DIFFERENT key cannot interleave and
	// land a stale snapshot over a newer one. These three were the
	// savers the round-2 fix missed.
	_ = s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.engagements, "", " ")
		return b, s.engagementsPath(), err
	})
}

func (s *Store) saveOutcomeStats() {
	// Through persist: it holds writeMu across marshal→write→rename,
	// so a concurrent save of a DIFFERENT key cannot interleave and
	// land a stale snapshot over a newer one. These three were the
	// savers the round-2 fix missed.
	_ = s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.outcomeStats, "", " ")
		return b, s.outcomeStatsPath(), err
	})
}

func (s *Store) ciFailedPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "ci_failed.json")
}

func (s *Store) saveCIFailed() {
	// Through persist: it holds writeMu across marshal→write→rename,
	// so a concurrent save of a DIFFERENT key cannot interleave and
	// land a stale snapshot over a newer one. These three were the
	// savers the round-2 fix missed.
	_ = s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.ciFailed, "", " ")
		return b, s.ciFailedPath(), err
	})
}

// loadOutcomeState loads the outcome files at Open (missing/corrupt = start fresh).
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
	if p := s.ciFailedPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &s.ciFailed)
		}
	}
	if s.ciFailed == nil {
		s.ciFailed = map[string]ciFailMark{}
	}
}
