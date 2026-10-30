package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
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
	// VerifySSHNS checks an SSHSIG in a named namespace (SSH sign-in and SSH
	// step-up). Defaults to ssh-keygen (VerifySSHSigNS).
	VerifySSHNS func(ctx context.Context, namespace, operator, pubKey string, payload []byte, sig string) error
	// Policy is the resolved dashboard.auth block; nil = DefaultPolicy.
	Policy *Policy
	// Audit records every auth event. Optional.
	Audit func(map[string]any)
	// Lifecycle fires conductor.* events (dashboard_enrolled, dashboard_reset).
	// Optional.
	Lifecycle func(event, operator, detail string)
	// DescribeStepup says in words what a step-up for (purpose, digest) by
	// session would confirm — the description the dashboard recorded when it
	// handed that session the digest (a config review, a proposal's action).
	// An SSH step-up binds it when it begins, and approve-login shows it before
	// it signs; a step-up nobody described is refused. "" when it doesn't
	// know. Credential changes are described here regardless.
	DescribeStepup func(session string, purpose Purpose, digest string) string
	// Now and Rand are injectable for deterministic tests.
	Now  func() time.Time
	Rand io.Reader
}

// Manager ties the store, the per-RP WebAuthn instances and the injected
// collaborators together. It is the one object server.go mounts.
type Manager struct {
	deps Deps

	muxOnce sync.Once
	mux     http.Handler                  // the auth routes behind sameOriginJSON, built once
	rpMu    sync.RWMutex                  // guards rps/wa (the tailnet RP is added after serving begins)
	rps     map[string]RP                 // by rpKey(listener, RP ID)
	wa      map[string]*webauthn.WebAuthn // by rpKey(listener, RP ID)
	lock    *lockout
	hash    chan struct{} // bounds concurrent argon2id hashes (each takes ~19 MiB)

	mu      sync.Mutex
	logins  map[string]*ceremony // sign-in challenges, by ceremony id
	enrolls map[string]*enrollCeremony
	stepups map[string]*stepupCeremony // in-flight step-up challenges, by ceremony id
	tokens  map[string]*stepupToken    // minted single-use step-up tokens, by token
	sshCh   map[string]*sshChallenge   // in-flight SSH sign-in / step-up challenges, by id
	sshCode map[string]string          // login code -> challenge id
	totpNew map[string]*totpPending    // TOTP enrolments awaiting a confirming code, by session id
	regs    map[string]*sessionReg     // signed-in passkey registrations, by ceremony id
}

// ceremony is an in-flight WebAuthn challenge (login or step-up pre-state).
type ceremony struct {
	rpID    string // the rpKey
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
	if deps.VerifySSHNS == nil {
		deps.VerifySSHNS = VerifySSHSigNS
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
		hash:    make(chan struct{}, 2),
		logins:  map[string]*ceremony{},
		enrolls: map[string]*enrollCeremony{},
		stepups: map[string]*stepupCeremony{},
		tokens:  map[string]*stepupToken{},
		sshCh:   map[string]*sshChallenge{},
		sshCode: map[string]string{},
		totpNew: map[string]*totpPending{},
		regs:    map[string]*sessionReg{},
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
	m.rps[rpKey(rp.Listener, rp.ID)] = rp
	m.wa[rpKey(rp.Listener, rp.ID)] = w
	return nil
}

// rpKey indexes an RP by the listener it serves and its id: two listeners may
// share an RP id (a loopback bind and the localhost listener) with different
// origins.
func rpKey(l Listener, id string) string { return string(l) + "|" + id }

// rpForListener returns the RP bound to a listener (the first by id).
func (m *Manager) rpForListener(l Listener) (RP, bool) {
	return m.rpFor(l, "")
}

// rpFor returns the RP for a request: on listener l, whose id is the request's
// host. host "" picks the listener's first RP (by id), for callers that don't
// have a request. A host with no RP (an IP literal, a plain-HTTP hostname) has
// none, and passkeys are not offered there.
func (m *Manager) rpFor(l Listener, host string) (RP, bool) {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	if host != "" {
		rp, ok := m.rps[rpKey(l, strings.ToLower(host))]
		return rp, ok
	}
	var best RP
	found := false
	for _, rp := range m.rps {
		if rp.Listener == l && (!found || rp.ID < best.ID) {
			best, found = rp, true
		}
	}
	return best, found
}

// waFor returns the WebAuthn instance for an rpKey, race-free.
func (m *Manager) waFor(key string) *webauthn.WebAuthn {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	return m.wa[key]
}

// rpByID returns the RP record for an rpKey, race-free.
func (m *Manager) rpByID(key string) (RP, bool) {
	m.rpMu.RLock()
	defer m.rpMu.RUnlock()
	rp, ok := m.rps[key]
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
	for id, c := range m.sshCh {
		if now.After(c.expires) {
			delete(m.sshCh, id)
			m.unindexCode(c)
		}
	}
	for id, p := range m.totpNew {
		if now.After(p.expires) {
			delete(m.totpNew, id)
		}
	}
	for id, r := range m.regs {
		if now.After(r.expires) {
			delete(m.regs, id)
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
