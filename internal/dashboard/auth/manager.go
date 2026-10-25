package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// RP describes one relying party the dashboard signs into: an RP ID (a bare
// host, never an IP or a scheme) and the origins a ceremony response may declare.
type RP struct {
	ID       string   // "localhost" or the MagicDNS name
	Origins  []string // "http://localhost:7788" | "https://box.tailnet.ts.net:7788"
	Listener Listener
}

// Deps are the Manager's injected collaborators. Everything that reaches outside
// the package is here, so tests supply fakes and a fixed clock (the kv-store
// lesson: an injectable clock makes TTL deterministic).
type Deps struct {
	Store *Store
	// Operators reports the configured operators and their authorized_keys
	// public key, for enrollment's SSH-signature gate and operator existence.
	Operators func() map[string]string
	// Session bounds a session (idle, absolute).
	SessionIdle time.Duration
	SessionMax  time.Duration
	// VerifySSH checks an SSHSIG over payload against operator's pubKey in the
	// enrollment namespace. Defaults to ssh-keygen (verifySSHSig); tests inject.
	VerifySSH func(ctx context.Context, operator, pubKey string, payload []byte, sig string) error
	// Audit records every auth event. Optional.
	Audit func(map[string]any)
	// Lifecycle fires conductor.* events (dashboard_enrolled, dashboard_reset).
	// Optional.
	Lifecycle func(event, operator, detail string)
	// Now and Rand are injectable for deterministic tests.
	Now  func() time.Time
	Rand io.Reader
}

// Manager ties the store, the per-RP WebAuthn instances and the injected
// collaborators together. It is the one object server.go mounts.
type Manager struct {
	deps Deps
	rpMu sync.RWMutex                  // guards rps/wa (the tailnet RP is added after serving begins)
	rps  map[string]RP                 // by RP ID
	wa   map[string]*webauthn.WebAuthn // by RP ID
	lock *lockout

	mu      sync.Mutex
	logins  map[string]*ceremony // sign-in challenges, by ceremony id
	enrolls map[string]*enrollCeremony
	stepups map[string]*stepupCeremony // in-flight step-up challenges, by ceremony id
	tokens  map[string]*stepupToken   // minted single-use step-up tokens, by token
}

// ceremony is an in-flight WebAuthn challenge (login or step-up pre-state).
type ceremony struct {
	rpID    string
	session webauthn.SessionData
	expires time.Time
}

// NewManager builds a Manager over the given RPs (one per bound listener).
func NewManager(deps Deps, rps ...RP) (*Manager, error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Rand == nil {
		deps.Rand = rand.Reader
	}
	if deps.VerifySSH == nil {
		deps.VerifySSH = verifySSHSig
	}
	if deps.SessionIdle <= 0 {
		deps.SessionIdle = 30 * time.Minute
	}
	if deps.SessionMax <= 0 {
		deps.SessionMax = 12 * time.Hour
	}
	m := &Manager{
		deps: deps, rps: map[string]RP{}, wa: map[string]*webauthn.WebAuthn{},
		lock:    newLockout(deps.Now),
		logins:  map[string]*ceremony{},
		enrolls: map[string]*enrollCeremony{},
		stepups: map[string]*stepupCeremony{},
		tokens:  map[string]*stepupToken{},
	}
	for _, rp := range rps {
		if err := m.addRP(rp); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// AddRP registers another relying party after construction — the tailnet RP,
// added once the MagicDNS name is discovered from tailscaled (design §6.2).
func (m *Manager) AddRP(rp RP) error {
	m.rpMu.Lock()
	defer m.rpMu.Unlock()
	return m.addRP(rp)
}

// addRP registers a relying party. Caller holds rpMu (or is in construction).
func (m *Manager) addRP(rp RP) error {
	w, err := webauthn.New(&webauthn.Config{
		RPID:          rp.ID,
		RPDisplayName: "conductor",
		RPOrigins:     rp.Origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
		},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		return fmt.Errorf("auth: webauthn for RP %q: %w", rp.ID, err)
	}
	m.rps[rp.ID] = rp
	m.wa[rp.ID] = w
	return nil
}

// rpForListener returns the RP bound to a listener (the first match).
func (m *Manager) rpForListener(l Listener) (RP, bool) {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	for _, rp := range m.rps {
		if rp.Listener == l {
			return rp, true
		}
	}
	return RP{}, false
}

// waFor returns the WebAuthn instance for an RP id, race-free.
func (m *Manager) waFor(id string) *webauthn.WebAuthn {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	return m.wa[id]
}

// rpByID returns the RP record for an id, race-free.
func (m *Manager) rpByID(id string) (RP, bool) {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	rp, ok := m.rps[id]
	return rp, ok
}

// allRPs returns a snapshot of the registered RPs.
func (m *Manager) allRPs() []RP {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	out := make([]RP, 0, len(m.rps))
	for _, rp := range m.rps {
		out = append(out, rp)
	}
	return out
}

// operatorPubKey returns an operator's configured public key, or false when the
// operator (or key) is not configured.
func (m *Manager) operatorPubKey(operator string) (string, bool) {
	if m.deps.Operators == nil {
		return "", false
	}
	k, ok := m.deps.Operators()[operator]
	if !ok || strings.TrimSpace(k) == "" {
		return "", false
	}
	return k, true
}

func (m *Manager) now() time.Time { return m.deps.Now() }

func (m *Manager) audit(e map[string]any) {
	if m.deps.Audit != nil {
		m.deps.Audit(e)
	}
}

// gcCeremonies drops expired in-flight ceremonies. Caller holds m.mu.
func (m *Manager) gcCeremonies() {
	now := m.now()
	for id, c := range m.logins {
		if now.After(c.expires) {
			delete(m.logins, id)
		}
	}
	for id, c := range m.enrolls {
		if now.After(c.expires) {
			delete(m.enrolls, id)
		}
	}
	for id, c := range m.stepups {
		if now.After(c.expires) {
			delete(m.stepups, id)
		}
	}
	for tok, t := range m.tokens {
		if now.After(t.expires) {
			delete(m.tokens, tok)
		}
	}
}

// randBytes reads n random bytes from the injected source.
func (m *Manager) randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(m.deps.Rand, b); err != nil {
		return nil, err
	}
	return b, nil
}
