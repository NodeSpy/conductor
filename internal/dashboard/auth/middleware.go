package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

// reqInfo is per-request context the listener fills in before the app handler
// runs: which interface the request arrived on, and (on the tailnet) the WhoIs
// node and login the peer resolved to. The auth layer reads it to bind sessions
// and pick the right RP.
type reqInfo struct {
	Listener  Listener
	Node      string    // tailnet node id (stable), empty on localhost
	User      string    // tailnet login name, empty on localhost
	Transport Transport // how the request arrived (cookie choice, passkey offer)
	Host      string    // the request's Host, port stripped, lowercased
}

// ConnInfo is what a listener wrapper knows about a request before the auth
// layer sees it.
type ConnInfo struct {
	Listener  Listener
	Node      string
	User      string
	Transport Transport // empty = the listener's default
}

type ctxKey int

const (
	ctxReq  ctxKey = iota // reqInfo
	ctxSess               // the resolved Session on a protected route
)

// WithReqInfo returns r carrying the listener/peer facts. server.go calls this in
// the listener wrapper.
func WithReqInfo(r *http.Request, l Listener, node, user string) *http.Request {
	return WithConn(r, ConnInfo{Listener: l, Node: node, User: user})
}

// WithConn returns r carrying the listener, peer and transport facts.
func WithConn(r *http.Request, c ConnInfo) *http.Request {
	t := c.Transport
	if t == "" {
		t = defaultTransport(c.Listener)
	}
	info := reqInfo{Listener: c.Listener, Node: c.Node, User: c.User, Transport: t, Host: hostOnly(r.Host)}
	return r.WithContext(context.WithValue(r.Context(), ctxReq, info))
}

func reqInfoFrom(r *http.Request) reqInfo {
	if v, ok := r.Context().Value(ctxReq).(reqInfo); ok {
		return v
	}
	return reqInfo{Listener: ListenerLocalhost, Transport: TransportLocalhost, Host: hostOnly(r.Host)}
}

// ConnOf returns the listener, peer and transport facts r arrived with.
func ConnOf(r *http.Request) ConnInfo {
	i := reqInfoFrom(r)
	return ConnInfo{Listener: i.Listener, Node: i.Node, User: i.User, Transport: i.Transport}
}

// SessionFrom returns the session a protected handler is running under.
func SessionFrom(r *http.Request) (Session, bool) {
	s, ok := r.Context().Value(ctxSess).(Session)
	return s, ok
}

// Protect gates /api behind a valid session and CSRF, and binds the session to
// the listener and node the request arrived on. It is the middleware every
// non-auth API route sits behind (design §6: Host → session → CSRF). The Host
// allowlist runs in each listener's wrapper upstream; a cross-site page can't
// pass the CSRF check, which needs a custom header (a preflight) and the token.
func (m *Manager) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := reqInfoFrom(r)
		sess, err := m.ValidateCookie(r, info.Listener, info.Node)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
			return
		}
		if err := checkCSRF(sess, r); err != nil {
			writeErr(w, http.StatusForbidden, "csrf", err.Error(), "reload the page to refresh the token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxSess, sess)))
	})
}

// SecurityHeaders sets the headers every dashboard response carries (design
// §6.4): a strict CSP with trusted types, clickjacking and rebinding defences,
// and no-store on the API. It wraps the whole server.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// script-src stays strict ('self' + trusted types) — agent-authored text is
		// the XSS vector, and it can never introduce a script. style-src allows
		// inline style ATTRIBUTES (the SPA uses them for layout); an inline style
		// cannot execute script, so this doesn't open the script vector, while
		// 'self' still bars remote stylesheets.
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; "+
				"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; "+
				"frame-ancestors 'none'; require-trusted-types-for 'script'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "publickey-credentials-get=(self), publickey-credentials-create=(self), camera=(), microphone=(), geolocation=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// writeErr writes the shared {error:{code,message,hint}} shape.
func writeErr(w http.ResponseWriter, code int, errCode, msg, hint string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": errCode, "message": msg, "hint": hint}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
