package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Enrollment lifetimes (design §6.3).
const (
	ticketTTL     = 10 * time.Minute
	enrollSkew    = 2 * time.Minute // freshness window on the signed enroll request
	maxCodeTries  = 3               // pairing-code failures that burn a ticket
)

// enrollCeremony is a redeemed ticket mid-registration: the operator it is for,
// the chosen RP's registration SessionData, and whether an existing-passkey
// assertion still has to clear (second+ passkey, design §6.3).
type enrollCeremony struct {
	operator    string
	rpID        string
	name        string // the passkey's display label, chosen at begin
	regSession  webauthn.SessionData
	assertReq   webauthn.SessionData // the "confirm with an existing passkey" challenge
	needAssert  bool
	registering bool // regSession is set and finish may proceed
	expires     time.Time
}

// EnrollPayload is the canonical text the CLI signs with the operator's SSH key.
// It names the operator and when it was issued, so a captured request can't be
// replayed outside a short window (design §6.3).
func EnrollPayload(operator string, issued time.Time) []byte {
	return []byte(fmt.Sprintf("conductor dashboard enroll v1\noperator: %s\nissued: %s\n", operator, issued.UTC().Format(time.RFC3339)))
}

// MintTicket verifies the operator's SSH signature over an EnrollPayload and, on
// success, mints a one-time enrollment ticket: a 256-bit secret (returned to the
// CLI to print in the URL fragment) and a pairing code. One live ticket per
// operator replaces any prior one. This runs on the control socket only.
func (m *Manager) MintTicket(ctx context.Context, operator string, issued time.Time, signature string) (secret, pairingCode string, expires time.Time, err error) {
	pub, ok := m.operatorPubKey(operator)
	if !ok {
		return "", "", time.Time{}, fmt.Errorf("unknown operator %q (or it has no ssh_key)", operator)
	}
	now := m.now()
	if d := now.Sub(issued); d < -enrollSkew || d > enrollSkew {
		return "", "", time.Time{}, fmt.Errorf("enrollment request is stale (issued %s)", issued.UTC().Format(time.RFC3339))
	}
	if err := m.deps.VerifySSH(ctx, operator, pub, EnrollPayload(operator, issued), signature); err != nil {
		return "", "", time.Time{}, err
	}
	sb, err := m.randBytes(32)
	if err != nil {
		return "", "", time.Time{}, err
	}
	secret = b64(sb)
	code, err := m.pairingCode()
	if err != nil {
		return "", "", time.Time{}, err
	}
	h := sha256.Sum256([]byte(secret))
	t := &EnrollTicket{
		Operator:    operator,
		SecretHash:  hex.EncodeToString(h[:]),
		PairingCode: code,
		Created:     now,
		Expires:     now.Add(ticketTTL),
	}
	if err := m.deps.Store.PutTicket(t); err != nil {
		return "", "", time.Time{}, err
	}
	m.audit(map[string]any{"event": "dashboard_enroll_ticket", "operator": operator})
	return secret, code, t.Expires, nil
}

// VoidTicket cancels an operator's live ticket (en-cancel, and `conductor
// dashboard reset`).
func (m *Manager) VoidTicket(operator string) {
	_ = m.deps.Store.DeleteTicket(operator)
}

// RedeemTicket validates the URL secret and pairing code and opens an enrollment
// ceremony. It returns the ceremony id, the operator and how many passkeys the
// operator already has (a second+ passkey needs an existing-passkey assertion).
func (m *Manager) RedeemTicket(secret, pairingCode string) (ceremonyID, operator string, existing int, err error) {
	h := sha256.Sum256([]byte(secret))
	want := hex.EncodeToString(h[:])
	// The URL carries only the secret, so find the ticket by its hash.
	var tk *EnrollTicket
	for op := range m.deps.Operators() {
		if t, ok := m.deps.Store.Ticket(op); ok && t.SecretHash == want {
			tk = t
			break
		}
	}
	if tk == nil {
		return "", "", 0, errors.New("this enrollment link is not valid")
	}
	if tk.Voided {
		return "", "", 0, errors.New("this enrollment link was cancelled")
	}
	if m.now().After(tk.Expires) {
		_ = m.deps.Store.DeleteTicket(tk.Operator)
		return "", "", 0, errors.New("this enrollment link has expired")
	}
	if pairingCode != tk.PairingCode {
		tk.CodeTries++
		if tk.CodeTries >= maxCodeTries {
			tk.Voided = true
		}
		_ = m.deps.Store.UpdateTicket(tk)
		m.audit(map[string]any{"event": "dashboard_enroll_code_fail", "operator": tk.Operator, "tries": tk.CodeTries})
		if tk.Voided {
			return "", "", 0, errors.New("wrong pairing code — the link is now void; run conductor dashboard enroll for a new one")
		}
		return "", "", 0, errors.New("wrong pairing code")
	}
	existing = len(m.deps.Store.CredentialsFor(tk.Operator))
	id, err := m.mintID()
	if err != nil {
		return "", "", 0, err
	}
	m.mu.Lock()
	m.gcCeremonies()
	m.enrolls[id] = &enrollCeremony{operator: tk.Operator, needAssert: existing > 0, expires: m.now().Add(ticketTTL)}
	m.mu.Unlock()
	return id, tk.Operator, existing, nil
}

// BeginEnroll continues a redeemed ceremony. For a second+ passkey it first
// requires an assertion from an existing passkey: called with assertion == nil it
// returns the assertion options (needAssert true); called with the assertion it
// validates it and then returns the registration options. rp selects which RP the
// new passkey registers under; name is its display label.
func (m *Manager) BeginEnroll(listener Listener, ceremonyID, rp, name string, assertion []byte) (needAssert bool, optionsJSON []byte, err error) {
	m.mu.Lock()
	ce, ok := m.enrolls[ceremonyID]
	m.mu.Unlock()
	if !ok {
		return false, nil, errors.New("unknown or expired enrollment")
	}
	if m.now().After(ce.expires) {
		return false, nil, errors.New("enrollment expired")
	}
	target, ok := m.rpByChoice(listener, rp)
	if !ok {
		return false, nil, fmt.Errorf("cannot register on %q from this listener", rp)
	}

	// Second+ passkey: an existing assertion gates it (purpose enroll.second).
	if ce.needAssert {
		if assertion == nil {
			w := m.wa[target.ID]
			opts, sess, err := w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
			if err != nil {
				return false, nil, err
			}
			m.mu.Lock()
			ce.assertReq = *sess
			m.mu.Unlock()
			b, err := json.Marshal(opts)
			return true, b, err
		}
		if err := m.validateExistingAssertion(target.ID, ce, assertion); err != nil {
			return false, nil, err
		}
		ce.needAssert = false
	}

	// Registration options for the new passkey.
	w := m.wa[target.ID]
	user := m.userFor(ce.operator)
	var excl []protocol.CredentialDescriptor
	for _, c := range user.creds {
		excl = append(excl, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: c.ID})
	}
	creation, sess, err := w.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
		}),
		webauthn.WithExclusions(excl),
	)
	if err != nil {
		return false, nil, err
	}
	m.mu.Lock()
	ce.rpID = target.ID
	ce.regSession = *sess
	ce.registering = true
	ce.name = name
	m.mu.Unlock()
	b, err := json.Marshal(creation)
	return false, b, err
}

// validateExistingAssertion checks the "confirm with an existing passkey"
// assertion against the operator's own credentials (design §6.3).
func (m *Manager) validateExistingAssertion(rpID string, ce *enrollCeremony, responseJSON []byte) error {
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(responseJSON))
	if err != nil {
		return fmt.Errorf("bad assertion: %w", err)
	}
	stored, ok := m.deps.Store.CredentialByRawID(rpID, parsed.RawID)
	if !ok || stored.Operator != ce.operator {
		return errors.New("that passkey is not one of yours")
	}
	w := m.wa[rpID]
	user := waUser{operator: ce.operator, creds: []webauthn.Credential{stored.Cred}}
	validated, err := w.ValidateLogin(user, ce.assertReq, parsed)
	if err != nil {
		return err
	}
	if validated.Authenticator.CloneWarning {
		_ = m.deps.Store.LockCredential(stored.ID)
		return errors.New("that passkey may be cloned and is now locked")
	}
	_ = m.deps.Store.UpdateCredentialUse(stored.ID, validated.Authenticator.SignCount)
	return nil
}

// FinishEnroll completes registration, stores the new credential, consumes the
// ticket, and audits + fires conductor.dashboard_enrolled (design §6.3).
func (m *Manager) FinishEnroll(ceremonyID, name string, responseJSON []byte) (*Credential, error) {
	m.mu.Lock()
	ce, ok := m.enrolls[ceremonyID]
	if ok && ce.registering {
		delete(m.enrolls, ceremonyID) // single use
	}
	m.mu.Unlock()
	if !ok || !ce.registering {
		return nil, errors.New("enrollment not ready to finish")
	}
	if m.now().After(ce.expires) {
		return nil, errors.New("enrollment expired")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(responseJSON))
	if err != nil {
		return nil, fmt.Errorf("bad registration: %w", err)
	}
	w := m.wa[ce.rpID]
	user := m.userFor(ce.operator)
	cred, err := w.CreateCredential(user, ce.regSession, parsed)
	if err != nil {
		return nil, err
	}
	id, err := m.mintID()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = ce.name
	}
	if strings.TrimSpace(name) == "" {
		name = "new passkey"
	}
	rp := m.rps[ce.rpID]
	c := &Credential{
		ID: id, Operator: ce.operator, Name: name, RPID: ce.rpID, Listener: rp.Listener,
		DeviceBound: deviceBound(cred), Created: m.now(), LastUsed: m.now(), Cred: *cred,
	}
	if err := m.deps.Store.AddCredential(c); err != nil {
		return nil, err
	}
	_ = m.deps.Store.DeleteTicket(ce.operator) // the ticket is spent
	m.audit(map[string]any{"event": "dashboard_enrolled", "operator": ce.operator, "credential": id, "rp": ce.rpID, "name": name})
	if m.deps.Lifecycle != nil {
		m.deps.Lifecycle("conductor.dashboard_enrolled", ce.operator, fmt.Sprintf("passkey %q on %s", name, ce.rpID))
	}
	return c, nil
}

// rpByChoice resolves the enroll "register on" choice to an RP. "localhost" and
// "tailnet"/"tailscale" name the two; the choice must be one the daemon actually
// binds (so a localhost-only box can't register a tailnet passkey nothing serves).
func (m *Manager) rpByChoice(listener Listener, choice string) (RP, bool) {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "", "localhost":
		return m.rpForListener(ListenerLocalhost)
	case "tailnet", "tailscale":
		return m.rpForListener(ListenerTailscale)
	default:
		return RP{}, false
	}
}

// pairingCode returns a human-readable "NNN-NNN" pairing code.
func (m *Manager) pairingCode() (string, error) {
	b, err := m.randBytes(4)
	if err != nil {
		return "", err
	}
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1_000_000
	return fmt.Sprintf("%03d-%03d", n/1000, n%1000), nil
}
