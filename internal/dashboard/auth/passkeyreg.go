package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// sessionReg is a passkey being registered by a signed-in session for the RP of
// the connection it is on — how an operator who first signed in with SSH on a
// remote HTTPS hostname gets a passkey for that hostname (the enroll link is
// localhost-only, and a browser can only create a credential for the origin it
// is on). The handler has already spent a step-up for PurposeCredential.
type sessionReg struct {
	session  string
	operator string
	rpKey    string
	name     string
	wa       webauthn.SessionData
	expires  time.Time
}

// BeginSessionPasskey starts registering a passkey on this connection's RP.
func (m *Manager) BeginSessionPasskey(r *http.Request, sess Session, name string) (string, []byte, error) {
	rp, ok := m.passkeyRP(r)
	if !ok {
		return "", nil, errors.New("passkeys need HTTPS (or localhost) on a hostname conductor serves a relying party for")
	}
	key := rpKey(rp.Listener, rp.ID)
	user := m.userFor(sess.Operator)
	var excl []protocol.CredentialDescriptor
	for _, c := range user.creds {
		excl = append(excl, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: c.ID})
	}
	creation, wsess, err := m.waFor(key).BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
		}),
		webauthn.WithExclusions(excl),
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
	m.regs[id] = &sessionReg{session: sess.ID, operator: sess.Operator, rpKey: key, name: name, wa: *wsess, expires: m.now().Add(ceremonyTTL)}
	m.mu.Unlock()
	b, err := json.Marshal(creation)
	return id, b, err
}

// FinishSessionPasskey completes a signed-in registration and stores the
// credential.
func (m *Manager) FinishSessionPasskey(sess Session, id, name string, responseJSON []byte) (*Credential, error) {
	m.mu.Lock()
	reg, ok := m.regs[id]
	if ok {
		delete(m.regs, id)
	}
	m.mu.Unlock()
	if !ok || m.now().After(reg.expires) {
		return nil, errors.New("passkey registration expired — start again")
	}
	if reg.session != sess.ID || reg.operator != sess.Operator {
		return nil, errors.New("passkey registration is for a different session")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(responseJSON))
	if err != nil {
		return nil, fmt.Errorf("bad registration: %w", err)
	}
	cred, err := m.waFor(reg.rpKey).CreateCredential(m.userFor(sess.Operator), reg.wa, parsed)
	if err != nil {
		return nil, err
	}
	cid, err := m.mintID()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = reg.name
	}
	if strings.TrimSpace(name) == "" {
		name = "new passkey"
	}
	rp, _ := m.rpByID(reg.rpKey)
	c := &Credential{
		ID: cid, Operator: sess.Operator, Name: name, RPID: rp.ID, Listener: rp.Listener,
		DeviceBound: deviceBound(cred), Created: m.now(), LastUsed: m.now(), Cred: *cred,
	}
	if err := m.deps.Store.AddCredential(c); err != nil {
		return nil, err
	}
	m.audit(map[string]any{"event": "dashboard_enrolled", "operator": sess.Operator, "credential": cid, "rp": rp.ID, "name": name, "via": "session"})
	if m.deps.Lifecycle != nil {
		m.deps.Lifecycle("conductor.dashboard_enrolled", sess.Operator, fmt.Sprintf("passkey %q on %s", name, rp.ID))
	}
	return c, nil
}

// passkeyRP is the RP a passkey ceremony on this request would use, or false
// when passkeys can't work here: passkey sign-in disabled, a connection that
// isn't a secure context, or a Host with no relying party.
func (m *Manager) passkeyRP(r *http.Request) (RP, bool) {
	info := reqInfoFrom(r)
	if !info.Transport.Secure() {
		return RP{}, false
	}
	return m.rpFor(info.Listener, info.Host)
}
