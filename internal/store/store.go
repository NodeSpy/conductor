// Package store persists paseo-conductor's dedup state and audit log.
//
// State is keyed per PR/issue ("owner/name#n"). It stays bounded: records are
// deleted when a PR closes, TTL-evicted when untouched, and LRU-capped as a
// backstop (see gc.go). The audit log is an append-only JSONL with size
// rotation (see audit.go).
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is the per-PR/issue dedup state.
type Record struct {
	HeadSHA   string               `json:"head_sha,omitempty"`
	Acted     map[string]string    `json:"acted,omitempty"`      // kind -> last acted dedup signature
	Attempts  map[string]int       `json:"attempts,omitempty"`   // "kind@head" -> count
	AttemptAt map[string]time.Time `json:"attempt_at,omitempty"` // "kind@head" -> last attempt time (for backoff)
	LastComID int64                `json:"last_comment_id,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// Store is the concurrency-safe state + audit store.
type Store struct {
	mu       sync.Mutex
	path     string
	runsPath string
	ttl      time.Duration
	maxPRs   int
	recs     map[string]*Record
	runs     map[string]*WorkflowRun
	audit    *auditLog
	now      func() time.Time
}

// Options configure a Store.
type Options struct {
	StatePath    string
	AuditPath    string
	TTL          time.Duration
	MaxPRs       int
	AuditMaxSize int64
}

// Open loads (or creates) the state file and prepares the audit log.
func Open(o Options) (*Store, error) {
	s := &Store{
		path:     o.StatePath,
		runsPath: filepath.Join(filepath.Dir(o.StatePath), "runs.json"),
		ttl:      o.TTL,
		maxPRs:   o.MaxPRs,
		recs:     map[string]*Record{},
		runs:     map[string]*WorkflowRun{},
		now:      time.Now,
	}
	if err := os.MkdirAll(filepath.Dir(o.StatePath), 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(o.StatePath); err == nil {
		_ = json.Unmarshal(b, &s.recs) // tolerate an empty/corrupt file: start fresh
		if s.recs == nil {
			s.recs = map[string]*Record{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if b, err := os.ReadFile(s.runsPath); err == nil {
		_ = json.Unmarshal(b, &s.runs)
		if s.runs == nil {
			s.runs = map[string]*WorkflowRun{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	a, err := openAudit(o.AuditPath, o.AuditMaxSize)
	if err != nil {
		return nil, err
	}
	s.audit = a
	return s, nil
}

func (s *Store) rec(key string) *Record {
	r := s.recs[key]
	if r == nil {
		r = &Record{Acted: map[string]string{}, Attempts: map[string]int{}}
		s.recs[key] = r
	}
	if r.Acted == nil {
		r.Acted = map[string]string{}
	}
	if r.Attempts == nil {
		r.Attempts = map[string]int{}
	}
	if r.AttemptAt == nil {
		r.AttemptAt = map[string]time.Time{}
	}
	return r
}

// LastCommentID returns the high-water mark of the newest PR comment already acted
// on for key (0 if none). The sweep's comment recovery uses it (via the engine) to
// skip comments already handled and only re-emit genuinely-missed newer ones.
func (s *Store) LastCommentID(key string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.recs[key]; r != nil {
		return r.LastComID
	}
	return 0
}

// AdvanceCommentID raises key's comment high-water mark to id (never lowers it).
func (s *Store) AdvanceCommentID(key string, id int64) error {
	s.mu.Lock()
	r := s.rec(key)
	if id <= r.LastComID {
		s.mu.Unlock()
		return nil
	}
	r.LastComID = id
	r.UpdatedAt = s.now()
	s.mu.Unlock()
	return s.save()
}

// LastSignature returns the last dedup signature acted on for (key, kind).
func (s *Store) LastSignature(key, kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.recs[key]; r != nil {
		return r.Acted[kind]
	}
	return ""
}

// Attempts returns how many times (key, kind) has been dispatched at head.
func (s *Store) Attempts(key, kind, head string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.recs[key]; r != nil {
		return r.Attempts[kind+"@"+head]
	}
	return 0
}

// RetryReady reports whether a live-gated (key, kind, head) is eligible to retry,
// and if not, how long until it is. Below the soft threshold it's always ready
// (the sweep paces it). At or past soft it enters a growing backoff since its last
// attempt: cooldown = base * factor^(attempts-soft), capped at max. A missing/zero
// last-attempt time (e.g. state written before backoff existed) is treated as
// ready — so a previously hard-capped PR self-heals on the next sweep.
func (s *Store) RetryReady(key, kind, head string, soft int, base time.Duration, factor int, max time.Duration) (ready bool, wait time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recs[key]
	if r == nil {
		return true, 0
	}
	k := kind + "@" + head
	n := r.Attempts[k]
	if n < soft {
		return true, 0
	}
	last := r.AttemptAt[k]
	if last.IsZero() {
		return true, 0
	}
	cd := base
	for i := 0; i < n-soft && cd < max; i++ {
		cd *= time.Duration(factor)
	}
	if cd > max {
		cd = max
	}
	elapsed := s.now().Sub(last)
	if elapsed >= cd {
		return true, 0
	}
	return false, cd - elapsed
}

// Record marks (key, kind) as acted on for signature sig at head, incrementing
// the per-head attempt counter and touching the record. Persists immediately.
// Use this only on a SUCCESSFUL dispatch — it consumes the dedup signature so
// the same state won't fire again.
func (s *Store) Record(key, kind, sig, head string) error {
	s.mu.Lock()
	r := s.rec(key)
	r.Acted[kind] = sig
	r.HeadSHA = head
	r.Attempts[kind+"@"+head]++
	r.AttemptAt[kind+"@"+head] = s.now()
	r.UpdatedAt = s.now()
	s.mu.Unlock()
	return s.save()
}

// RecordAttempt counts a dispatch attempt at head WITHOUT consuming the dedup
// signature — for a FAILED dispatch, so the same state retries next time (bounded
// by the per-head attempt cap → escalation) instead of being suppressed forever.
func (s *Store) RecordAttempt(key, kind, head string) error {
	s.mu.Lock()
	r := s.rec(key)
	r.HeadSHA = head
	r.Attempts[kind+"@"+head]++
	r.AttemptAt[kind+"@"+head] = s.now()
	r.UpdatedAt = s.now()
	s.mu.Unlock()
	return s.save()
}

// SetNow overrides the clock — for tests exercising time-dependent behavior
// (retry backoff, TTL). Not used in production.
func (s *Store) SetNow(fn func() time.Time) {
	s.mu.Lock()
	s.now = fn
	s.mu.Unlock()
}

// Touch updates the record's last-seen time (keeps it alive against TTL).
func (s *Store) Touch(key string) {
	s.mu.Lock()
	s.rec(key).UpdatedAt = s.now()
	s.mu.Unlock()
}

// Delete removes a PR/issue record (e.g. on close/merge). Persists.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	_, existed := s.recs[key]
	delete(s.recs, key)
	s.mu.Unlock()
	if !existed {
		return nil
	}
	return s.save()
}

// Audit appends an entry to the audit log.
func (s *Store) Audit(entry map[string]any) {
	if s.audit == nil {
		return
	}
	if _, ok := entry["ts"]; !ok {
		entry["ts"] = s.now().UTC().Format(time.RFC3339)
	}
	s.audit.write(entry)
}

// Close flushes and closes the audit log.
func (s *Store) Close() error {
	if s.audit != nil {
		return s.audit.close()
	}
	return nil
}

func (s *Store) save() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.recs, "", "  ")
	path := s.path
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
