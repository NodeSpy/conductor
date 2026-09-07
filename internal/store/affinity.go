package store

import (
	"encoding/json"
	"os"
	"time"

	"github.com/NodeSpy/conductor/internal/controller"
)

// AffinityRecord is one persisted session-affinity binding: (agent profile,
// rendered key) → the runtime session that owns the conversation. Stored in a
// sibling affinity.json (beside runs.json/sessions.json/holds.json — the
// conductor's own state, never a user stores: entry) so a keyed session
// survives a restart or auto-update: the next same-key event resumes the
// session by id instead of spawning a fresh agent.
type AffinityRecord struct {
	Agent      string    `json:"agent"`
	Key        string    `json:"key"`
	Controller string    `json:"controller"` // runtime that owns the session
	SessionID  string    `json:"session_id"`
	Created    time.Time `json:"created"`
	LastUsed   time.Time `json:"last_used"`
}

// Assert *Store satisfies the affinity registry's persistence contract.
var _ controller.AffinityStore = (*Store)(nil)

func affinityKey(agent, key string) string { return agent + "\x00" + key }

// PutAffinity upserts one binding and persists immediately.
func (s *Store) PutAffinity(ref controller.AffinityRef) error {
	s.mu.Lock()
	s.affinity[affinityKey(ref.Agent, ref.Key)] = &AffinityRecord{
		Agent:      ref.Agent,
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
func (s *Store) DeleteAffinity(agent, key string) error {
	s.mu.Lock()
	k := affinityKey(agent, key)
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
		out = append(out, controller.AffinityRef{
			Agent:      r.Agent,
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
