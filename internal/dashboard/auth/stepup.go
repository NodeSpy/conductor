package auth

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// stepupChallengeLabel domain-separates the step-up challenge from any other
// WebAuthn ceremony, and binds the assertion to the purpose and the request
// digest so the signature can be re-verified offline (design §6.5, §6.6).
const stepupChallengeLabel = "conductor-dashboard-stepup-v1"

// stepupCeremony is an in-flight step-up assertion, bound to the session that
// asked, the purpose and the digest of the exact request.
type stepupCeremony struct {
	rpID     string
	session  string // the signed-in session id that is stepping up
	operator string
	purpose  Purpose
	digest   string // hex sha256 of the request body (or the approval digest)
	nonce    []byte
	wa       webauthn.SessionData
	expires  time.Time
}

// stepupToken is a minted single-use step-up grant, bound to {session, purpose,
// digest} and spent by RequireStepup (design §6.5). It carries the raw assertion
// so a decision it authorizes can be re-verified offline (design §6.6).
type stepupToken struct {
	session  string
	operator string
	purpose  Purpose
	digest   string
	evidence StepupEvidence
	expires  time.Time
}

// StepupEvidence is what verified a step-up. Method names it; for a passkey it
// is the credential and the raw authenticator data / client data / signature
// (base64url), enough to re-verify the signature offline against the stored
// public key (design §6.6); for SSH it is the signed challenge text, its
// namespace and the armored SSHSIG, re-verifiable with ssh-keygen against
// operators.<name>.ssh_key. A TOTP or password step-up leaves nothing a third
// party could re-verify, and records only the method.
type StepupEvidence struct {
	Method            Method
	CredID            string
	AuthenticatorData string
	ClientDataJSON    string
	Signature         string
	SSHNamespace      string
	SSHPayload        string
	SSHSignature      string
}

// stepupChallenge derives the WebAuthn challenge from the label, the per-ceremony
// nonce, the purpose and the digest, so the operator's signature commits to all of
// them.
func stepupChallenge(nonce []byte, purpose Purpose, digest string) []byte {
	h := sha256.New()
	h.Write([]byte(stepupChallengeLabel))
	h.Write([]byte{0})
	h.Write(nonce)
	h.Write([]byte{0})
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(digest))
	return h.Sum(nil)
}

// BeginStepup starts a step-up for the signed-in session over {purpose, digest}.
// The assertion is scoped to the session operator's credentials, and its
// challenge commits to the purpose and digest (design §6.5). It returns a
// ceremony id and the assertion options.
func (m *Manager) BeginStepup(listener Listener, sess Session, purpose Purpose, digest string) (ceremonyID string, optionsJSON []byte, err error) {
	return m.BeginStepupFor(listener, "", sess, purpose, digest)
}

// BeginStepupFor is BeginStepup on the RP for (listener, host).
func (m *Manager) BeginStepupFor(listener Listener, host string, sess Session, purpose Purpose, digest string) (ceremonyID string, optionsJSON []byte, err error) {
	if !m.policy().StepupAllowed(MethodPasskey) {
		return "", nil, errors.New("passkey step-up is not enabled (dashboard.auth.stepup)")
	}
	rp, ok := m.rpFor(listener, host)
	if !ok {
		return "", nil, errors.New("passkeys are not available on this connection")
	}
	if digest == "" {
		return "", nil, errors.New("step-up needs a request digest")
	}
	user := m.userFor(sess.Operator)
	if len(user.creds) == 0 {
		return "", nil, errors.New("you have no passkey that can sign this")
	}
	var allow []protocol.CredentialDescriptor
	for _, c := range user.creds {
		allow = append(allow, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: c.ID})
	}
	nonce, err := m.randBytes(32)
	if err != nil {
		return "", nil, err
	}
	key := rpKey(rp.Listener, rp.ID)
	w := m.waFor(key)
	opts, session, err := w.BeginLogin(user,
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithAllowedCredentials(allow),
		webauthn.WithChallenge(stepupChallenge(nonce, purpose, digest)),
	)
	if err != nil {
		return "", nil, err
	}
	id, err := m.mintID()
	if err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	m.gcCeremonies()
	m.stepups[id] = &stepupCeremony{
		rpID: key, session: sess.ID, operator: sess.Operator, purpose: purpose,
		digest: digest, nonce: nonce, wa: *session, expires: m.now().Add(ceremonyTTL),
	}
	m.mu.Unlock()
	b, err := json.Marshal(opts)
	return id, b, err
}

// FinishStepup validates a step-up assertion and mints a single-use step-up
// token bound to {session, purpose, digest}. The ceremony is consumed.
func (m *Manager) FinishStepup(ceremonyID string, sess Session, responseJSON []byte) (token string, err error) {
	m.mu.Lock()
	ce, ok := m.stepups[ceremonyID]
	if ok {
		delete(m.stepups, ceremonyID) // single use
	}
	m.mu.Unlock()
	if !ok {
		return "", errors.New("unknown or expired step-up challenge")
	}
	if m.now().After(ce.expires) {
		return "", errors.New("step-up challenge expired")
	}
	if ce.session != sess.ID || ce.operator != sess.Operator {
		return "", errors.New("step-up challenge is for a different session")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(responseJSON))
	if err != nil {
		return "", fmt.Errorf("bad assertion: %w", err)
	}
	rp, _ := m.rpByID(ce.rpID)
	stored, found := m.deps.Store.CredentialByRawID(rp.ID, parsed.RawID)
	if !found || stored.Operator != ce.operator {
		return "", errors.New("that passkey is not yours")
	}
	w := m.waFor(ce.rpID)
	user := waUser{operator: ce.operator, creds: []webauthn.Credential{stored.Cred}}
	validated, err := w.ValidateLogin(user, ce.wa, parsed)
	if err != nil {
		return "", err
	}
	if validated.Authenticator.CloneWarning {
		_ = m.deps.Store.LockCredential(stored.ID)
		return "", errors.New("that passkey may be cloned and is now locked")
	}
	_ = m.deps.Store.UpdateCredentialUse(stored.ID, validated.Authenticator.SignCount)
	return m.mintStepup(sess, ce.purpose, ce.digest, StepupEvidence{
		Method:            MethodPasskey,
		CredID:            stored.ID,
		AuthenticatorData: b64(parsed.Raw.AssertionResponse.AuthenticatorData),
		ClientDataJSON:    b64(parsed.Raw.AssertionResponse.ClientDataJSON),
		Signature:         b64(parsed.Raw.AssertionResponse.Signature),
	})
}

// mintStepup issues the single-use step-up token every method ends in, bound to
// {session, purpose, digest} with a short expiry. The caller has verified the
// evidence; callers that spend the token (RequireStepup) never see which method
// minted it, only the evidence it carries for the audit.
func (m *Manager) mintStepup(sess Session, purpose Purpose, digest string, ev StepupEvidence) (string, error) {
	if digest == "" {
		return "", errors.New("step-up needs a request digest")
	}
	if !m.policy().StepupAllowed(ev.Method) {
		return "", fmt.Errorf("%s step-up is not enabled (dashboard.auth.stepup)", ev.Method)
	}
	tb, err := m.randBytes(32)
	if err != nil {
		return "", err
	}
	token := b64(tb)
	m.deps.Store.MarkStepup(sess.ID, m.now())
	m.mu.Lock()
	m.gcCeremonies()
	m.tokens[token] = &stepupToken{
		session: sess.ID, operator: sess.Operator, purpose: purpose,
		digest: digest, evidence: ev, expires: m.now().Add(ceremonyTTL),
	}
	m.mu.Unlock()
	m.audit(map[string]any{"event": "dashboard_stepup", "operator": sess.Operator, "purpose": string(purpose),
		"method": string(ev.Method), "credential": ev.CredID})
	return token, nil
}

// CredentialDigest is the step-up digest for adding or removing a sign-in
// credential (PurposeCredential): it names the operator and the action, so a
// token minted to add a TOTP authenticator can't remove a password.
func CredentialDigest(operator, action string) string {
	return DigestOf([]byte("conductor-dashboard-credential-v1\x00" + operator + "\x00" + action))
}

// RequireStepup spends a step-up token: it must exist, be unexpired, belong to
// this session, and match the purpose and digest of the request it authorizes.
// It returns the credential id that signed (for the audit line). It is
// single-use: a valid token is removed whether or not it matched, so a captured
// token can't be probed against several requests (design §6.5).
func (m *Manager) RequireStepup(sess Session, purpose Purpose, digest, token string) (StepupEvidence, error) {
	if token == "" {
		return StepupEvidence{}, errors.New("this action needs your passkey")
	}
	m.mu.Lock()
	t, ok := m.tokens[token]
	if ok {
		delete(m.tokens, token)
	}
	m.mu.Unlock()
	if !ok {
		return StepupEvidence{}, errors.New("step-up token is not valid")
	}
	if m.now().After(t.expires) {
		return StepupEvidence{}, errors.New("step-up token has expired")
	}
	if t.session != sess.ID || t.operator != sess.Operator {
		return StepupEvidence{}, errors.New("step-up token is for a different session")
	}
	if t.purpose != purpose {
		return StepupEvidence{}, fmt.Errorf("step-up token is for %q, not %q", t.purpose, purpose)
	}
	if subtle.ConstantTimeCompare([]byte(t.digest), []byte(digest)) != 1 {
		return StepupEvidence{}, errors.New("step-up token does not match this request")
	}
	return t.evidence, nil
}

// DigestOf is the canonical digest of a request body: hex sha256. Both the
// step-up ceremony and the endpoint that enforces it compute it the same way.
func DigestOf(body []byte) string {
	h := sha256.Sum256(body)
	return fmt.Sprintf("%x", h[:])
}
