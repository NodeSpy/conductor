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
	Listener Listener
	Node     string // tailnet node id (stable), empty on localhost
	User     string // tailnet login name, empty on localhost
}

type ctxKey int

const (
	ctxReq  ctxKey = iota // reqInfo
	ctxSess               // the resolved Session on a protected route
)

// WithReqInfo returns r carrying the listener/peer facts. server.go calls this in
// the listener wrapper.
func WithReqInfo(r *http.Request, l Listener, node, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxReq, reqInfo{Listener: l, Node: node, User: user}))
}

func reqInfoFrom(r *http.Request) reqInfo {
	if v, ok := r.Context().Value(ctxReq).(reqInfo); ok {
		return v
	}
	return reqInfo{Listener: ListenerLocalhost}
}

// SessionFrom returns the session a protected handler is running under.
func SessionFrom(r *http.Request) (Session, bool) {
	s, ok := r.Context().Value(ctxSess).(Session)
	return s, ok
}

// Protect gates /api behind a valid session and CSRF, and binds the session to
// the listener and node the request arrived on. It is the middleware every
// non-auth API route sits behind (design §6: Host → Origin → session → CSRF, with
// Host/Origin done in the listener wrapper upstream).
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
