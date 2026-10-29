package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// ceremonyTTL bounds a login / step-up challenge (design §6.3: 2 min).
const ceremonyTTL = 2 * time.Minute

// waUser adapts an operator (and the credentials we scope into the ceremony) to
// go-webauthn's User interface. The handle is a deterministic per-operator value;
// discoverable login maps back to the operator by the raw credential id, so the
// handle is only cross-checked, never reversed.
type waUser struct {
	operator string
	creds    []webauthn.Credential
}

func (u waUser) WebAuthnID() []byte {
	h := sha256.Sum256([]byte("conductor-dashboard-operator:" + u.operator))
	return h[:]
}
func (u waUser) WebAuthnName() string                       { return u.operator }
func (u waUser) WebAuthnDisplayName() string                { return u.operator }
func (u waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// userFor builds a waUser for an operator from the store.
func (m *Manager) userFor(operator string) waUser {
	var creds []webauthn.Credential
	for _, c := range m.deps.Store.CredentialsFor(operator) {
		if c.Locked {
			continue // a locked credential can't be presented
		}
		creds = append(creds, c.Cred)
	}
	return waUser{operator: operator, creds: creds}
}

// --- sign-in (discoverable) ---

// BeginLogin starts a discoverable passkey sign-in on the listener's RP. It
// returns a ceremony id (opaque, carried back on finish) and the publicKey
// request options as JSON for navigator.credentials.get.
func (m *Manager) BeginLogin(listener Listener) (ceremonyID string, optionsJSON []byte, err error) {
	return m.BeginLoginFor(listener, "")
}

// BeginLoginFor starts a passkey sign-in on the RP for (listener, host).
func (m *Manager) BeginLoginFor(listener Listener, host string) (ceremonyID string, optionsJSON []byte, err error) {
	rp, ok := m.rpFor(listener, host)
	if !ok {
		return "", nil, fmt.Errorf("passkeys are not available on %q (no relying party for this host)", host)
	}
	key := rpKey(rp.Listener, rp.ID)
	w := m.waFor(key)
	assertion, session, err := w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, err
	}
	id, err := m.mintID()
	if err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	m.gcCeremonies()
	m.logins[id] = &ceremony{rpID: key, session: *session, expires: m.now().Add(ceremonyTTL)}
	m.mu.Unlock()
	b, err := json.Marshal(assertion)
	if err != nil {
		return "", nil, err
	}
	return id, b, nil
}

// FinishLogin validates a sign-in assertion and returns the operator and the
// credential that signed. A sign-count regression locks the credential and is
// refused (design §6.3). It is single-use: the ceremony is consumed.
func (m *Manager) FinishLogin(ceremonyID string, responseJSON []byte) (operator, credID string, cloneLocked bool, err error) {
	m.mu.Lock()
	c, ok := m.logins[ceremonyID]
	if ok {
		delete(m.logins, ceremonyID) // single use, even on failure
	}
	m.mu.Unlock()
	if !ok {
		return "", "", false, errors.New("unknown or expired sign-in challenge")
	}
	if m.now().After(c.expires) {
		return "", "", false, errors.New("sign-in challenge expired")
	}
	stored, validated, err := m.validateDiscoverable(c.rpID, c.session, responseJSON)
	if err != nil {
		return "", "", false, err
	}
	if validated.Authenticator.CloneWarning {
		_ = m.deps.Store.LockCredential(stored.ID)
		m.audit(map[string]any{"event": "dashboard_clone_detected", "operator": stored.Operator, "credential": stored.ID})
		return "", "", true, errors.New("sign count went backwards: this passkey may be cloned and is now locked")
	}
	if err := m.deps.Store.UpdateCredentialUse(stored.ID, validated.Authenticator.SignCount); err != nil {
		return "", "", false, err
	}
	return stored.Operator, stored.ID, false, nil
}

// validateDiscoverable verifies a discoverable-login assertion against the one
// credential it names, scoped to THIS RP so a credential enrolled for localhost
// cannot be replayed against the tailnet RP. It returns the stored credential
// (for its id/operator) and the go-webauthn verdict (for the clone warning and
// the new sign count).
func (m *Manager) validateDiscoverable(key string, session webauthn.SessionData, responseJSON []byte) (*Credential, *webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(responseJSON))
	if err != nil {
		return nil, nil, fmt.Errorf("bad assertion: %w", err)
	}
	rp, ok := m.rpByID(key)
	if !ok {
		return nil, nil, errors.New("unknown relying party")
	}
	stored, found := m.deps.Store.CredentialByRawID(rp.ID, parsed.RawID)
	if !found {
		return nil, nil, errors.New("no such credential on this listener")
	}
	if stored.Locked {
		return nil, nil, errors.New("this credential is locked")
	}
	w := m.waFor(key)
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		return waUser{operator: stored.Operator, creds: []webauthn.Credential{stored.Cred}}, nil
	}
	validated, err := w.ValidateDiscoverableLogin(handler, session, parsed)
	if err != nil {
		return nil, nil, err
	}
	return stored, validated, nil
}

// --- shared helpers ---

// mintID returns a URL-safe random id for a ceremony, credential or session.
func (m *Manager) mintID() (string, error) {
	b, err := m.randBytes(16)
	if err != nil {
		return "", err
	}
	return b64(b), nil
}

// deviceBound reports whether a freshly created credential stays on its device
// (not backup-eligible / not a synced passkey), which the Access screen shows and
// the device-bound approval policy can require.
func deviceBound(c *webauthn.Credential) bool {
	return !c.Flags.BackupEligible
}
