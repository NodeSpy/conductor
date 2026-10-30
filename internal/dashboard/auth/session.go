package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// errNoSession is returned when a request carries no valid session.
var errNoSession = errors.New("not signed in")

// CreateSession mints a fresh session for a signed-in operator and returns the
// Set-Cookie to write. The cookie value is "<id>.<secret>"; only the secret's
// hash is stored, so the Sessions list can never be turned back into a cookie.
func (m *Manager) CreateSession(operator string, l Listener, node, device, addr, credID string) (*http.Cookie, Session, error) {
	return m.createSession(Session{
		Operator: operator, Listener: l, Node: node, Device: device, Addr: addr, CredID: credID,
		Method: MethodPasskey, Transport: defaultTransport(l),
	})
}

// CreateSessionFor mints a session for a request that just signed in by method:
// the listener, node, transport, host, device and address come from the request.
func (m *Manager) CreateSessionFor(r *http.Request, operator string, method Method, credID string) (*http.Cookie, Session, error) {
	info := reqInfoFrom(r)
	return m.createSession(Session{
		Operator: operator, Listener: info.Listener, Node: info.Node,
		Device: deviceLabel(r), Addr: r.RemoteAddr, CredID: credID,
		Method: method, Transport: info.Transport, Host: info.Host,
	})
}

// createSession fills the id, secret, CSRF token and lifetimes into a session
// template, stores it and returns the cookie for its transport.
func (m *Manager) createSession(tmpl Session) (*http.Cookie, Session, error) {
	idb, err := m.randBytes(16)
	if err != nil {
		return nil, Session{}, err
	}
	secretB, err := m.randBytes(32)
	if err != nil {
		return nil, Session{}, err
	}
	csrfB, err := m.randBytes(32)
	if err != nil {
		return nil, Session{}, err
	}
	now := m.now()
	sess := tmpl
	sess.Cred = m.credFingerprint(sess.Operator, sess.EffectiveMethod())
	sess.ID, sess.CSRF = b64(idb), b64(csrfB)
	sess.Created, sess.LastSeen, sess.Expires = now, now, now.Add(m.deps.SessionMax)
	secret := b64(secretB)
	if err := m.deps.Store.PutSession(sess, secret); err != nil {
		return nil, Session{}, err
	}
	m.audit(map[string]any{"event": "dashboard_signin", "operator": sess.Operator, "session": sess.ID,
		"listener": string(sess.Listener), "method": string(sess.EffectiveMethod()), "transport": string(sess.Transport)})
	return m.cookie(sess.Listener, sessTransport(sess), sess.ID+"."+secret, sess.Expires), sess, nil
}

// sessTransport is the transport a session's cookie lives on.
func sessTransport(s Session) Transport {
	if s.Transport == "" {
		return defaultTransport(s.Listener)
	}
	return s.Transport
}

// RotateSecret issues a new cookie secret for an existing session (fixation
// resistance on step-up), keeping the session id so any step-up token bound to it
// stays valid. It returns the Set-Cookie to write.
func (m *Manager) RotateSecret(sess Session) (*http.Cookie, error) {
	secretB, err := m.randBytes(32)
	if err != nil {
		return nil, err
	}
	secret := b64(secretB)
	if err := m.deps.Store.PutSession(sess, secret); err != nil {
		return nil, err
	}
	return m.cookie(sess.Listener, sessTransport(sess), sess.ID+"."+secret, sess.Expires), nil
}

// ValidateCookie resolves the request's session cookie to a live Session,
// enforcing the idle and absolute lifetimes and the listener/node binding
// (design §6.3). It touches LastSeen on success.
func (m *Manager) ValidateCookie(r *http.Request, l Listener, node string) (Session, error) {
	transport := reqInfoFrom(r).Transport
	// Only the cookie for this listener and transport is read: a plain-HTTP
	// cookie is never accepted on a secure connection, nor the reverse.
	ck, err := r.Cookie(cookieName(l, transport))
	if err != nil || ck.Value == "" {
		return Session{}, errNoSession
	}
	id, secret, ok := strings.Cut(ck.Value, ".")
	if !ok || id == "" || secret == "" {
		return Session{}, errNoSession
	}
	sess, ok := m.deps.Store.Session(id)
	if !ok {
		return Session{}, errNoSession
	}
	if !m.deps.Store.VerifySessionSecret(id, secret) {
		return Session{}, errNoSession
	}
	now := m.now()
	if why := m.sessionEnded(sess, now); why != "" {
		_ = m.deps.Store.DeleteSession(id)
		m.audit(map[string]any{"event": "dashboard_session_ended", "operator": sess.Operator, "session": sess.ID, "why": why})
		return Session{}, errors.New(why)
	}
	// A cookie is bound to the listener it was created on: a localhost cookie
	// never works on the tailnet, and vice versa.
	if sess.Listener != l {
		return Session{}, errors.New("session is for a different listener")
	}
	// On the tailnet, the cookie is also bound to the WhoIs node, so a cookie
	// replayed from another tailnet node is refused (design §6.3/§6.7).
	if l == ListenerTailscale && sess.Node != "" && sess.Node != node {
		return Session{}, errors.New("session is for a different tailnet node")
	}
	if sessTransport(sess) != transport {
		return Session{}, errors.New("session is for a different transport")
	}
	m.deps.Store.TouchSession(id, now)
	sess.LastSeen = now
	return sess, nil
}

// expiresAt is when a session ends at the latest: its stored ceiling, or
// session.max after it began if that is sooner (a shortened max applies to
// sessions that already exist).
func (m *Manager) expiresAt(s Session) time.Time {
	if cap := s.Created.Add(m.deps.SessionMax); cap.Before(s.Expires) {
		return cap
	}
	return s.Expires
}

// sessionEnded says why a session is no longer live, or "".
func (m *Manager) sessionEnded(s Session, now time.Time) string {
	switch {
	case now.After(m.expiresAt(s)):
		return "session expired"
	case now.Sub(s.LastSeen) > m.deps.SessionIdle:
		return "session idle too long"
	case !m.operatorExists(s.Operator):
		return "the operator is no longer configured"
	}
	switch s.EffectiveMethod() {
	case MethodSSH, MethodPassword:
		if fp := m.credFingerprint(s.Operator, s.EffectiveMethod()); fp == "" || fp != s.Cred {
			return "the credential this session signed in with has changed — sign in again"
		}
	}
	return ""
}

// credFingerprint identifies the credential a session by method rests on: the
// operator's configured ssh_key for SSH; the password hash and authenticator
// secret for a password. "" when there is none, or for a passkey (removing a
// passkey deletes its sessions).
func (m *Manager) credFingerprint(operator string, method Method) string {
	var material string
	switch method {
	case MethodSSH:
		if m.deps.Operators == nil {
			return ""
		}
		f := strings.Fields(m.deps.Operators()[operator])
		if len(f) < 2 {
			return ""
		}
		material = "ssh\x00" + f[0] + "\x00" + f[1]
	case MethodPassword:
		rec, _ := m.deps.Store.OperatorSecret(operator)
		if rec.PasswordHash == "" {
			return ""
		}
		material = "password\x00" + rec.PasswordHash + "\x00" + rec.TOTPSecret
	default:
		return ""
	}
	return hashHex(material)
}

// rebind moves the operator's own session onto the credential it just
// changed (with a step-up): the other sessions resting on the old one end.
func (m *Manager) rebind(sess Session) {
	if fp := m.credFingerprint(sess.Operator, sess.EffectiveMethod()); fp != "" {
		m.deps.Store.SetSessionCred(sess.ID, fp)
	}
}

// Logout revokes the request's session and returns a clearing cookie.
func (m *Manager) Logout(r *http.Request) *http.Cookie {
	info := reqInfoFrom(r)
	if ck, err := r.Cookie(cookieName(info.Listener, info.Transport)); err == nil {
		if id, _, ok := strings.Cut(ck.Value, "."); ok {
			if sess, ok := m.deps.Store.Session(id); ok {
				m.audit(map[string]any{"event": "dashboard_logout", "operator": sess.Operator, "session": id})
			}
			_ = m.deps.Store.DeleteSession(id)
		}
	}
	return m.clearCookie(info.Listener, info.Transport)
}

// cookie builds the session cookie for a transport. On a secure context it is
// the __Host cookie: Secure, HttpOnly, SameSite=Strict, Path=/, no Domain (the
// prefix forbids one); browsers treat http://localhost as secure, so Secure is
// accepted there too (design §6.3). On plain HTTP off loopback the browser would
// drop a Secure cookie, so it is the unprefixed HttpOnly, SameSite=Strict one.
func (m *Manager) cookie(l Listener, t Transport, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name: cookieName(l, t), Value: value, Path: "/",
		Secure: t.Secure(), HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: expires,
	}
}

func (m *Manager) clearCookie(l Listener, t Transport) *http.Cookie {
	return &http.Cookie{
		Name: cookieName(l, t), Value: "", Path: "/",
		Secure: t.Secure(), HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: -1,
	}
}

// checkCSRF enforces the per-session double-submit token on a mutating request
// (design §6.4). GET/HEAD never mutate and are exempt.
func checkCSRF(sess Session, r *http.Request) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	got := r.Header.Get(CSRFHeader)
	if got == "" || got != sess.CSRF {
		return errors.New("missing or bad CSRF token")
	}
	return nil
}
