package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Store is the dashboard's durable auth state: enrolled credentials, live
// sessions and the one-shot enrollment ticket. It lives under <state>/dashboard
// (0700, files 0600) so another local user can't read a session secret or a
// public key (design §6.7). Everything is guarded by one mutex; writes are
// atomic (temp + rename).
//
// Credentials and sessions persist across a daemon re-exec (a config apply and
// every auto-update re-exec the daemon, design §5.2) so an operator is not
// signed out by routine reloads. In-flight ceremonies (login/enroll/step-up
// challenges) are intentionally in-memory only: a ceremony interrupted by a
// reload simply restarts.
type Store struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	creds   map[string]*Credential // by credential ID
	sess    map[string]*sessionRec // by session ID
	tickets map[string]*EnrollTicket // by operator (one live ticket each)
}

// sessionRec is a Session plus the secret hash that authenticates its cookie.
type sessionRec struct {
	Session    Session `json:"session"`
	SecretHash string  `json:"secret_hash"`
}

type persisted struct {
	Creds   []*Credential  `json:"credentials"`
	Sess    []*sessionRec  `json:"sessions"`
	Tickets []*EnrollTicket `json:"tickets"`
}

// OpenStore loads (or creates) the auth store rooted at dir. now defaults to
// time.Now.
func OpenStore(dir string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: state dir: %w", err)
	}
	// Tighten an inherited-looser mode (MkdirAll won't narrow an existing dir).
	_ = os.Chmod(dir, 0o700)
	s := &Store{dir: dir, now: now, creds: map[string]*Credential{}, sess: map[string]*sessionRec{}, tickets: map[string]*EnrollTicket{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) file() string { return filepath.Join(s.dir, "auth.json") }

func (s *Store) load() error {
	b, err := os.ReadFile(s.file())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: read state: %w", err)
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("auth: parse state: %w", err)
	}
	for _, c := range p.Creds {
		s.creds[c.ID] = c
	}
	for _, r := range p.Sess {
		s.sess[r.Session.ID] = r
	}
	for _, t := range p.Tickets {
		s.tickets[t.Operator] = t
	}
	return nil
}

// save writes the durable state atomically. Caller holds mu.
func (s *Store) save() error {
	p := persisted{}
	for _, c := range s.creds {
		p.Creds = append(p.Creds, c)
	}
	for _, r := range s.sess {
		p.Sess = append(p.Sess, r)
	}
	for _, t := range s.tickets {
		p.Tickets = append(p.Tickets, t)
	}
	sort.Slice(p.Creds, func(i, j int) bool { return p.Creds[i].Created.Before(p.Creds[j].Created) })
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

// --- credentials ---

// AddCredential stores a newly enrolled passkey.
func (s *Store) AddCredential(c *Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[c.ID] = c
	return s.save()
}

// CredentialsFor returns an operator's credentials, oldest first.
func (s *Store) CredentialsFor(operator string) []*Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Credential
	for _, c := range s.creds {
		if c.Operator == operator {
			out = append(out, clone(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// AllCredentials returns every credential, oldest first.
func (s *Store) AllCredentials() []*Credential {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Credential
	for _, c := range s.creds {
		out = append(out, clone(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// CredentialByRawID finds a credential by its raw WebAuthn credential id, scoped
// to one RP so a credential enrolled for localhost can't be presented on the
// tailnet RP.
func (s *Store) CredentialByRawID(rpID string, raw []byte) (*Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.creds {
		if c.RPID == rpID && len(c.Cred.ID) == len(raw) && string(c.Cred.ID) == string(raw) {
			return clone(c), true
		}
	}
	return nil, false
}

// UpdateCredentialUse records a successful assertion: sign count, last-used and
// the use counter.
func (s *Store) UpdateCredentialUse(id string, signCount uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[id]
	if !ok {
		return fmt.Errorf("no credential %s", id)
	}
	c.Cred.Authenticator.SignCount = signCount
	c.LastUsed = s.now()
	c.Uses++
	return s.save()
}

// LockCredential flags a credential as locked (a sign-count regression: a
// possible clone, design §6.3).
func (s *Store) LockCredential(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[id]
	if !ok {
		return fmt.Errorf("no credential %s", id)
	}
	c.Locked = true
	return s.save()
}

// DeleteCredential removes a passkey and ends every session it started.
func (s *Store) DeleteCredential(id string) (operator string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[id]
	if !ok {
		return "", fmt.Errorf("no credential %s", id)
	}
	operator = c.Operator
	delete(s.creds, id)
	for sid, r := range s.sess {
		if r.Session.CredID == id {
			delete(s.sess, sid)
		}
	}
	return operator, s.save()
}

// --- sessions ---

// PutSession stores a session with the hash of its cookie secret.
func (s *Store) PutSession(sess Session, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess[sess.ID] = &sessionRec{Session: sess, SecretHash: hashHex(secret)}
	return s.save()
}

// Session returns a session by ID.
func (s *Store) Session(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sess[id]
	if !ok {
		return Session{}, false
	}
	return r.Session, true
}

// VerifySessionSecret checks the cookie secret against the stored hash.
func (s *Store) VerifySessionSecret(id, secret string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sess[id]
	return ok && r.SecretHash == hashHex(secret)
}

// TouchSession updates LastSeen (idle tracking). It returns false when the
// session is gone.
func (s *Store) TouchSession(id string, t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.sess[id]
	if !ok {
		return false
	}
	r.Session.LastSeen = t
	_ = s.save()
	return true
}

// MarkStepup records a fresh step-up time on the session (for the freshness
// readout and rotation).
func (s *Store) MarkStepup(id string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.sess[id]; ok {
		r.Session.StepupAt = t
		_ = s.save()
	}
}

// SessionsFor lists an operator's sessions, newest first.
func (s *Store) SessionsFor(operator string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, r := range s.sess {
		if r.Session.Operator == operator {
			out = append(out, r.Session)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// DeleteSession revokes one session.
func (s *Store) DeleteSession(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sess, id)
	return s.save()
}

// DeleteSessionsFor signs an operator out everywhere.
func (s *Store) DeleteSessionsFor(operator string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.sess {
		if r.Session.Operator == operator {
			delete(s.sess, id)
		}
	}
	return s.save()
}

// --- enrollment tickets ---

// PutTicket stores (and replaces) an operator's one live enrollment ticket.
func (s *Store) PutTicket(t *EnrollTicket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.Operator] = t
	return s.save()
}

// Ticket returns an operator's live ticket.
func (s *Store) Ticket(operator string) (*EnrollTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[operator]
	if !ok {
		return nil, false
	}
	cp := *t
	return &cp, true
}

// UpdateTicket replaces a ticket in place (code tries, voided).
func (s *Store) UpdateTicket(t *EnrollTicket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.Operator] = t
	return s.save()
}

// DeleteTicket consumes/voids an operator's ticket.
func (s *Store) DeleteTicket(operator string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tickets, operator)
	return s.save()
}

// --- helpers ---

func clone(c *Credential) *Credential {
	cp := *c
	return &cp
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
