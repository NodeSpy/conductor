package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// Method names a way to sign in or to step up (dashboard-auth-methods design).
// passkey, ssh and password sign in; totp is the password's second factor and,
// on its own, a step-up method.
type Method string

const (
	MethodPasskey  Method = "passkey"
	MethodSSH      Method = "ssh"
	MethodPassword Method = "password"
	MethodTOTP     Method = "totp"
)

// Policy is the resolved `dashboard.auth:` block: which methods sign in, whether
// a password needs a TOTP code, and which methods satisfy a step-up. The config
// layer resolves the defaults; a nil Deps.Policy means DefaultPolicy.
type Policy struct {
	Methods              []Method // sign-in methods
	PasswordRequiresTOTP bool     // the strong default; false is the explicit loosen
	Stepup               []Method // step-up methods
	// ConfigStepup is which config applies need a step-up: "all" (the
	// default) or "guarded" (all but edits confined to the inert paths). The
	// dashboard enforces it; the auth layer only reports it.
	ConfigStepup string
}

// DefaultPolicy is the policy an install without an `auth:` block gets: every
// sign-in method, password only with TOTP, and step-up by passkey, SSH or TOTP.
func DefaultPolicy() Policy {
	return Policy{
		Methods:              []Method{MethodPasskey, MethodSSH, MethodPassword},
		PasswordRequiresTOTP: true,
		Stepup:               []Method{MethodPasskey, MethodSSH, MethodTOTP},
		ConfigStepup:         "all",
	}
}

// SignIn reports whether m is an enabled sign-in method.
func (p Policy) SignIn(m Method) bool {
	if m == MethodTOTP {
		return false // a second factor, never a sign-in on its own
	}
	return hasMethod(p.Methods, m)
}

// StepupAllowed reports whether m satisfies a step-up. A password re-entry
// counts only when the password-only knob is on: with TOTP required, the
// password alone is not a full credential, so it can't authorize a sensitive
// write either.
func (p Policy) StepupAllowed(m Method) bool {
	if m == MethodPassword && (p.PasswordRequiresTOTP || !p.SignIn(MethodPassword)) {
		return false
	}
	return hasMethod(p.Stepup, m)
}

func hasMethod(list []Method, m Method) bool {
	for _, x := range list {
		if x == m {
			return true
		}
	}
	return false
}

// policy returns the Manager's policy (the default when none was injected).
func (m *Manager) policy() Policy {
	if m.deps.Policy != nil {
		return *m.deps.Policy
	}
	return DefaultPolicy()
}

// Transport is how a request reached the dashboard. It decides the session
// cookie (a Secure cookie is dropped by the browser on plain HTTP off localhost)
// and whether passkeys can be offered (WebAuthn needs a secure context).
type Transport string

const (
	TransportHTTPS     Transport = "https"     // TLS
	TransportLocalhost Transport = "localhost" // plain HTTP on loopback: a secure context
	TransportHTTP      Transport = "http"      // plain HTTP off loopback: not a secure context
)

// Secure reports whether the browser treats the origin as a secure context.
func (t Transport) Secure() bool { return t == TransportHTTPS || t == TransportLocalhost }

// Scheme is the URL scheme for the transport.
func (t Transport) Scheme() string {
	if t == TransportHTTPS {
		return "https"
	}
	return "http"
}

// PlainSessionCookie is the session cookie on a plain-HTTP, non-loopback
// connection. The browser drops a Secure cookie there, and the `__Host-` prefix
// requires Secure, so the cookie is HttpOnly + SameSite=Strict without either.
// It is sniffable on the wire; that is the cost the plain-HTTP acknowledgment
// accepts.
const PlainSessionCookie = "conductor_session"

// cookieName is the session cookie name for a listener and transport. Cookies
// ignore the port, so two listeners answering on one hostname (a loopback bind
// named localhost and the localhost listener) would overwrite each other's
// cookie under one name; a bind listener's cookie carries a suffix derived from
// its address.
func cookieName(l Listener, t Transport) string {
	name := PlainSessionCookie
	if t.Secure() {
		name = SessionCookie
	}
	if strings.HasPrefix(string(l), "bind:") {
		sum := sha256.Sum256([]byte(l))
		name += "_" + hex.EncodeToString(sum[:4])
	}
	return name
}

// defaultTransport is the transport a listener implies when the caller did not
// say (the localhost listener is loopback; the tailnet listener is TLS unless
// `tls: off`, which the server wrapper tags explicitly).
func defaultTransport(l Listener) Transport {
	if l == ListenerTailscale {
		return TransportHTTPS
	}
	return TransportLocalhost
}

// hostOnly returns the request Host without its port, lowercased, with any
// trailing dot and IPv6 brackets removed.
func hostOnly(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	return strings.ToLower(h)
}

// requestOrigin is the browser origin a request came from, as the server sees
// it: scheme from the transport, host:port from the (allow-listed) Host header.
func requestOrigin(r *http.Request) string {
	info := reqInfoFrom(r)
	return info.Transport.Scheme() + "://" + strings.ToLower(r.Host)
}
