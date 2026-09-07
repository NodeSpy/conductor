package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RunHistory is one execution's durable record (#36 §20): per-step inputs /
// outputs / status / timing / cost, plus the pinned trigger+action a
// user-driven retry re-runs from. Step I/O is persisted secret-scrubbed by
// the flow runner (the same scrubbing the crash-resume checkpoints get)
// before it reaches this package. One JSON file per run under
// <state dir>/history/, GC'd by retention (age + count).
type RunHistory struct {
	ID       string    `json:"id"`
	RunID    string    `json:"run_id,omitempty"` // the WorkflowRun id (kind:key — not unique across runs)
	Kind     string    `json:"kind"`
	Variant  string    `json:"variant,omitempty"` // the trigger's name:
	On       string    `json:"on,omitempty"`      // the trigger's on:
	Repo     string    `json:"repo,omitempty"`
	Number   int       `json:"number,omitempty"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	// Status: running | ok | failed | retried.
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	FailedStep string `json:"failed_step,omitempty"`
	// RetryOf backlinks a retry to the execution it re-ran.
	RetryOf string `json:"retry_of,omitempty"`
	// Spend totals (#36 §14).
	Tokens     int     `json:"tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	ApproxCost bool    `json:"approx_cost,omitempty"`
	// Trigger/Action are the pinned raw inputs (tokens stripped, like
	// WorkflowRun) a retry rehydrates.
	Trigger json.RawMessage `json:"trigger,omitempty"`
	Action  json.RawMessage `json:"action,omitempty"`
	Steps   []StepRecord    `json:"steps,omitempty"`
}

// StepRecord is one top-level step's execution record.
type StepRecord struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	// Status: ok | failed | skipped.
	Status     string         `json:"status"`
	Started    time.Time      `json:"started,omitzero"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Inputs     map[string]any `json:"inputs,omitempty"`  // rendered options / prompt (scrubbed)
	Outputs    map[string]any `json:"outputs,omitempty"` // step outputs (scrubbed, checkpoint form)
	Error      string         `json:"error,omitempty"`
	// ContinuedOnError marks a failed step the workflow ran past.
	ContinuedOnError bool `json:"continued_on_error,omitempty"`
	// Agent-step spend (#36 §14).
	Tokens  int     `json:"tokens,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
}

// Step returns the record with the given step id.
func (h *RunHistory) Step(id string) (StepRecord, bool) {
	for _, s := range h.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return StepRecord{}, false
}

// History retention defaults: enough to inspect the recent past without the
// directory growing unbounded.
const (
	DefaultHistoryMaxAge  = 14 * 24 * time.Hour
	DefaultHistoryMaxRuns = 500
)

// historyMu guards prune-vs-write races within this process.
var historyMu sync.Mutex

// historyDir derives the run-history directory from the store's state path.
func (s *Store) historyDir() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "history")
}

// PutHistory upserts one run's record (its own file, temp+rename) and lazily
// prunes by retention.
func (s *Store) PutHistory(rec RunHistory) error {
	dir := s.historyDir()
	if dir == "" || rec.ID == "" {
		return nil
	}
	if err := WriteHistory(dir, rec); err != nil {
		return err
	}
	s.maybePruneHistory()
	return nil
}

// GetHistory loads one run's record by id.
func (s *Store) GetHistory(id string) (RunHistory, bool) {
	rec, err := ReadHistory(s.historyDir(), id)
	return rec, err == nil
}

// ListHistory returns the most recent records, newest first (0 = all).
func (s *Store) ListHistory(limit int) []RunHistory {
	return ListHistoryDir(s.historyDir(), limit)
}

// maybePruneHistory prunes at most every 10 minutes.
func (s *Store) maybePruneHistory() {
	historyMu.Lock()
	due := time.Since(s.historyPruned) > 10*time.Minute
	if due {
		s.historyPruned = time.Now()
	}
	historyMu.Unlock()
	if due {
		_, _ = PruneHistoryDir(s.historyDir(), s.historyMaxAge, s.historyMaxRuns)
	}
}

// ---------------------------------------------------------------------------
// Directory-level helpers: standalone so the CLI (`conductor runs`) reads the
// history without opening — and contending on — the live daemon's store.
// ---------------------------------------------------------------------------

// historyFile maps an id to its file, refusing path-hostile ids.
func historyFile(dir, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("history: bad run id %q", id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// WriteHistory writes one record into dir (temp+rename).
func WriteHistory(dir string, rec RunHistory) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path, err := historyFile(dir, rec.ID)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadHistory loads one record by id.
func ReadHistory(dir, id string) (RunHistory, error) {
	path, err := historyFile(dir, id)
	if err != nil {
		return RunHistory{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RunHistory{}, fmt.Errorf("history: run %q: %w", id, err)
	}
	var rec RunHistory
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunHistory{}, fmt.Errorf("history: run %q: %w", id, err)
	}
	return rec, nil
}

// ListHistoryDir returns records newest-first (by Started), capped at limit
// (0 = all). Unreadable files are skipped.
func ListHistoryDir(dir string, limit int) []RunHistory {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []RunHistory
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		if rec, err := ReadHistory(dir, name); err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// PruneHistoryDir enforces retention: records older than maxAge go, then the
// oldest beyond maxRuns (0 disables that bound). Returns how many it removed.
func PruneHistoryDir(dir string, maxAge time.Duration, maxRuns int) (int, error) {
	if maxAge <= 0 {
		maxAge = DefaultHistoryMaxAge
	}
	recs := ListHistoryDir(dir, 0) // newest first
	cut := time.Now().Add(-maxAge)
	removed := 0
	for i, rec := range recs {
		tooOld := rec.Started.Before(cut)
		overCount := maxRuns > 0 && i >= maxRuns
		if !tooOld && !overCount {
			continue
		}
		if path, err := historyFile(dir, rec.ID); err == nil {
			if os.Remove(path) == nil {
				removed++
			}
		}
	}
	return removed, nil
}
