package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	// Sig is an HMAC-SHA256 over the record (Sig cleared), keyed by a
	// per-installation key next to the history files. Retry verifies it
	// before trusting pinned outputs (#36 review M8); display paths don't.
	Sig string `json:"sig,omitempty"`
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

// historyKey loads the per-installation HMAC key next to the history files,
// generating one (0600) on first use. The key lives beside the records it
// signs: an attacker who can already write the daemon's private state dir is
// outside the threat model — the signature catches everyone else (a doctored
// record smuggled in over a sync, a partial restore, a truncated copy).
func historyKey(dir string) ([]byte, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	path := filepath.Join(dir, ".hmac-key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return key, nil
}

// signHistory computes the record's HMAC over its canonical JSON (Sig
// cleared, compact marshal — struct field order is deterministic).
func signHistory(key []byte, rec RunHistory) (string, error) {
	rec.Sig = ""
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// WriteHistory writes one record into dir (temp+rename), signed.
func WriteHistory(dir string, rec RunHistory) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path, err := historyFile(dir, rec.ID)
	if err != nil {
		return err
	}
	if key, err := historyKey(dir); err == nil {
		if sig, err := signHistory(key, rec); err == nil {
			rec.Sig = sig
		}
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

// ReadHistoryVerified loads one record and verifies its HMAC before
// returning it. This is the read the retry path uses: a record with a
// missing or wrong signature (edited on disk, smuggled in) is rejected
// rather than replayed with attacker-chosen pinned outputs (#36 review M8).
// Display paths (list/detail) stay on the unverified read.
func ReadHistoryVerified(dir, id string) (RunHistory, error) {
	rec, err := ReadHistory(dir, id)
	if err != nil {
		return RunHistory{}, err
	}
	key, err := historyKey(dir)
	if err != nil {
		return RunHistory{}, fmt.Errorf("history: run %q: no signing key: %w", id, err)
	}
	want, err := signHistory(key, rec)
	if err != nil {
		return RunHistory{}, err
	}
	if rec.Sig == "" || !hmac.Equal([]byte(rec.Sig), []byte(want)) {
		return RunHistory{}, fmt.Errorf("history: run %q failed integrity verification — the record was modified on disk (or predates signing); refusing to trust its pinned inputs", id)
	}
	return rec, nil
}

// GetHistoryVerified is ReadHistoryVerified against the store's history dir.
func (s *Store) GetHistoryVerified(id string) (RunHistory, error) {
	return ReadHistoryVerified(s.historyDir(), id)
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
		// A still-running record is live state, not history: its checkpoints
		// back an in-flight run and its resume base. Deleting it out from under
		// the running flow would strand or corrupt that run, so never prune one
		// on age or count — it becomes eligible only once it has finished
		// (#57 M6). Status is the reliable signal here; Finished is not always
		// stamped on older finished records, so keying off it would wrongly
		// exempt them.
		if rec.Status == "running" {
			continue
		}
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
