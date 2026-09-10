package store

import (
	"encoding/json"
	"os"
	"time"

	"github.com/NodeSpy/conductor/internal/controller"
)

// AffinityRecord is one persisted session-affinity binding:
// (runtime, model, rendered key) → the runtime session that owns the
// conversation. Stored in a sibling affinity.json (beside
// runs.json/sessions.json/holds.json — the conductor's own state, never a
// user stores: entry) so a keyed session survives a restart or auto-update:
// the next same-key event resumes the session by id instead of spawning a
// fresh agent.
//
// The binding was re-keyed from (agent, key) when `agents:` was removed
// (docs/design/agents-removal.md §3). A record written by an older build has
// no runtime/model and simply does not match a new binding — the next event
// starts a fresh session, which is the correct outcome for a partition that
// genuinely changed. Sessions are short-lived (idle_ttl defaults to 24h), so
// nothing durable is lost; a track record would have been another matter.
type AffinityRecord struct {
	Runtime    string    `json:"runtime"`
	Model      string    `json:"model,omitempty"`
	Key        string    `json:"key"`
	Controller string    `json:"controller"` // runtime implementation that owns the session
	SessionID  string    `json:"session_id"`
	Created    time.Time `json:"created"`
	LastUsed   time.Time `json:"last_used"`
}

// Assert *Store satisfies the affinity registry's persistence contract.
var _ controller.AffinityStore = (*Store)(nil)

func affinityKey(runtime, model, key string) string {
	return runtime + "\x00" + model + "\x00" + key
}

// PutAffinity upserts one binding and persists immediately.
func (s *Store) PutAffinity(ref controller.AffinityRef) error {
	s.mu.Lock()
	s.affinity[affinityKey(ref.Runtime, ref.Model, ref.Key)] = &AffinityRecord{
		Runtime:    ref.Runtime,
		Model:      ref.Model,
		Key:        ref.Key,
		Controller: ref.Controller,
		SessionID:  ref.SessionID,
		Created:    ref.Created,
		LastUsed:   ref.LastUsed,
	}
	s.mu.Unlock()
	return s.saveAffinity()
}

// DeleteAffinity removes one binding (eviction). Persists.
func (s *Store) DeleteAffinity(runtime, model, key string) error {
	s.mu.Lock()
	k := affinityKey(runtime, model, key)
	_, existed := s.affinity[k]
	delete(s.affinity, k)
	s.mu.Unlock()
	if !existed {
		return nil
	}
	return s.saveAffinity()
}

// Affinities returns every persisted binding (startup restore).
func (s *Store) Affinities() []controller.AffinityRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]controller.AffinityRef, 0, len(s.affinity))
	for _, r := range s.affinity {
		if r.Runtime == "" {
			continue // a pre-re-key record: no runtime/model to bind against
		}
		out = append(out, controller.AffinityRef{
			Runtime:    r.Runtime,
			Model:      r.Model,
			Key:        r.Key,
			Controller: r.Controller,
			SessionID:  r.SessionID,
			Created:    r.Created,
			LastUsed:   r.LastUsed,
		})
	}
	return out
}

// saveAffinity persists the binding map (best-effort atomic via temp+rename).
func (s *Store) saveAffinity() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.affinity, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.affinityPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.affinityPath)
}
