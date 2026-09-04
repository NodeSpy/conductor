package store

import (
	"encoding/json"
	"os"
	"time"

	"github.com/NodeSpy/conductor/internal/controller"
)

// SessionRecord is the persisted pointer to a live/resumable agent session bound
// to a PR/issue, so an interactive hand-off survives a conductor restart: the
// process that held the live session is gone, but the record lets the broker
// resume it by id (ACP session/load; paseo re-binds the agent id) and funnel the
// next follow-up to it instead of spawning a duplicate agent. It's stored keyed
// per PR/issue ("owner/name#n") in a sibling sessions.json, mirroring runs.json.
// No secrets are persisted.
type SessionRecord struct {
	PRKey      string    `json:"pr_key"`
	Controller string    `json:"controller"` // controller name that owns the session
	SessionID  string    `json:"session_id"` // the controller's session/agent id
	Model      string    `json:"model"`      // session_model (native|resumable|oneshot)
	UpdatedAt  time.Time `json:"updated_at"`
}

// The store persists the broker's PR→session map. Assert *Store satisfies the
// broker's SessionStore contract so a signature drift fails the build here.
var _ controller.SessionStore = (*Store)(nil)

// PutSession upserts the session record for a PR and persists immediately.
func (s *Store) PutSession(ref controller.SessionRef) error {
	s.mu.Lock()
	rec := &SessionRecord{
		PRKey:      ref.PRKey,
		Controller: ref.Controller,
		SessionID:  ref.SessionID,
		Model:      string(ref.Model),
		UpdatedAt:  s.now(),
	}
	s.sessions[ref.PRKey] = rec
	s.mu.Unlock()
	return s.saveSessions()
}

// DeleteSession removes a PR's session record (on discard/close). Persists.
func (s *Store) DeleteSession(prKey string) error {
	s.mu.Lock()
	_, existed := s.sessions[prKey]
	delete(s.sessions, prKey)
	s.mu.Unlock()
	if !existed {
		return nil
	}
	return s.saveSessions()
}

// Sessions returns a snapshot of every persisted session ref (for startup
// restore of the broker's PR→session map).
func (s *Store) Sessions() []controller.SessionRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]controller.SessionRef, 0, len(s.sessions))
	for _, r := range s.sessions {
		out = append(out, controller.SessionRef{
			PRKey:      r.PRKey,
			Controller: r.Controller,
			SessionID:  r.SessionID,
			Model:      controller.SessionModel(r.Model),
			UpdatedAt:  r.UpdatedAt,
		})
	}
	return out
}

// saveSessions persists the sessions map (best-effort atomic via temp+rename).
func (s *Store) saveSessions() error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.sessions, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.sessionsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.sessionsPath)
}
