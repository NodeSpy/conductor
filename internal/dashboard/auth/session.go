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
	sess := Session{
		ID: b64(idb), Operator: operator, Listener: l, Node: node,
		Device: device, Addr: addr, CSRF: b64(csrfB),
		Created: now, LastSeen: now, Expires: now.Add(m.deps.SessionMax), CredID: credID,
	}
	secret := b64(secretB)
	if err := m.deps.Store.PutSession(sess, secret); err != nil {
		return nil, Session{}, err
	}
	m.audit(map[string]any{"event": "dashboard_signin", "operator": operator, "session": sess.ID, "listener": string(l)})
	return m.cookie(sess.ID+"."+secret, sess.Expires), sess, nil
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
	return m.cookie(sess.ID+"."+secret, sess.Expires), nil
}

// ValidateCookie resolves the request's session cookie to a live Session,
// enforcing the idle and absolute lifetimes and the listener/node binding
// (design §6.3). It touches LastSeen on success.
func (m *Manager) ValidateCookie(r *http.Request, l Listener, node string) (Session, error) {
	ck, err := r.Cookie(SessionCookie)
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
	if now.After(sess.Expires) {
		_ = m.deps.Store.DeleteSession(id)
		return Session{}, errors.New("session expired")
	}
	if now.Sub(sess.LastSeen) > m.deps.SessionIdle {
		_ = m.deps.Store.DeleteSession(id)
		return Session{}, errors.New("session idle too long")
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
	m.deps.Store.TouchSession(id, now)
	sess.LastSeen = now
	return sess, nil
}

// Logout revokes the request's session and returns a clearing cookie.
func (m *Manager) Logout(r *http.Request) *http.Cookie {
	if ck, err := r.Cookie(SessionCookie); err == nil {
		if id, _, ok := strings.Cut(ck.Value, "."); ok {
			if sess, ok := m.deps.Store.Session(id); ok {
				m.audit(map[string]any{"event": "dashboard_logout", "operator": sess.Operator, "session": id})
			}
			_ = m.deps.Store.DeleteSession(id)
		}
	}
	return m.clearCookie()
}

// cookie builds the __Host session cookie: Secure, HttpOnly, SameSite=Strict,
// Path=/, no Domain (the prefix forbids one). On http://localhost browsers treat
// the origin as secure, so Secure is accepted there too (design §6.3).
func (m *Manager) cookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: expires,
	}
}

func (m *Manager) clearCookie() *http.Cookie {
	return &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
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
