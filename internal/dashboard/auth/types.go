// Package auth is the dashboard's authentication and authorization layer
// (design §6). It owns passkey sign-in (WebAuthn), operator enrollment over the
// control socket, revocable sessions behind a __Host cookie, CSRF, step-up
// assertions for sensitive writes, lockout, and the Access API (passkeys and
// sessions CRUD). It is deliberately decoupled from the daemon: everything it
// needs — the operators' public keys, the audit sink, the clock, randomness —
// is injected, so the whole package is testable against a software authenticator
// with no daemon, socket or network.
//
// Trust roots:
//   - a passkey assertion with user verification, over a server-minted
//     challenge, is the only way to sign in or to step up;
//   - enrollment is gated by an SSH signature over a one-time ticket, verified
//     against operators.<name>.ssh_key — the same trust root as SSH-signed
//     approvals (internal/bot/approve.go), in its own namespace so a signature
//     made for an approval can never enroll a key.
package auth

import (
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// EnrollNamespace is the SSHSIG namespace an enrollment ticket is signed in. It
// is distinct from the bot-approval namespace so neither signature can be
// replayed as the other.
const EnrollNamespace = "conductor-dashboard-enroll"

// SessionCookie is the session cookie name. The `__Host-` prefix forces
// Secure, path=/ and no Domain, and is honoured on http://localhost too, so the
// same name is used on both listeners (design §6.3).
const SessionCookie = "__Host-conductor_session"

// CSRFHeader carries the per-session double-submit token on every mutating
// request (design §6.4).
const CSRFHeader = "X-Conductor-CSRF"

// StepupHeader carries a single-use step-up token on a sensitive write
// (design §5.1, §6.5).
const StepupHeader = "X-Conductor-Stepup"

// Listener names which interface a request arrived on. A session, a cookie and
// a credential are each bound to one (design §6.3: "a cookie replayed from
// another tailnet node is refused").
type Listener string

const (
	ListenerLocalhost Listener = "localhost"
	ListenerTailscale Listener = "tailscale"
)

// Credential is one enrolled passkey: the go-webauthn credential (public key,
// sign count, flags) plus the operator it belongs to and the display metadata
// the Access screen shows. SignCount mirrors cred.Authenticator.SignCount and is
// the authority a clone check compares against (design §6.3).
type Credential struct {
	ID          string              `json:"id"` // short, URL-safe handle for the API
	Operator    string              `json:"operator"`
	Name        string              `json:"name"` // operator-chosen ("phone · Face ID")
	RPID        string              `json:"rp_id"`
	Listener    Listener            `json:"listener"`     // which listener's RP it registered under
	DeviceBound bool                `json:"device_bound"` // platform attachment / no backup eligibility
	Created     time.Time           `json:"created"`
	LastUsed    time.Time           `json:"last_used"`
	Uses        int                 `json:"uses"`
	Locked      bool                `json:"locked"` // a sign-count regression locks the credential
	Cred        webauthn.Credential `json:"cred"`
}

// Session is one signed-in browser. It is revocable and bound to the listener
// (and, on the tailnet, the WhoIs node) it was created on (design §6.3).
type Session struct {
	ID       string    `json:"id"`
	Operator string    `json:"operator"`
	Listener Listener  `json:"listener"`
	Node     string    `json:"node,omitempty"` // tailnet WhoIs node id, when on the tailnet
	Device   string    `json:"device"`         // a human label from the User-Agent
	Addr     string    `json:"addr"`           // the remote address at sign-in
	CSRF     string    `json:"csrf"`           // the double-submit token
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	Expires  time.Time `json:"expires"`   // absolute ceiling
	StepupAt time.Time `json:"stepup_at"` // last fresh assertion, for the freshness readout
	CredID   string    `json:"cred_id"`   // the credential that signed in
	// Method is how the session signed in (passkey, ssh, password); empty on a
	// session created before methods existed, which was a passkey.
	Method Method `json:"method,omitempty"`
	// Transport is how the browser reached the dashboard at sign-in. The cookie
	// is only accepted back on the same transport; empty (an older session) means
	// a secure one (localhost or the tailnet's TLS).
	Transport Transport `json:"transport,omitempty"`
	Host      string    `json:"host,omitempty"` // the Host it signed in on
	// Cred fingerprints the credential an SSH or password session rests on
	// (the operator's ssh_key; the password hash and authenticator secret).
	// When it changes, the session ends (credFingerprint).
	Cred string `json:"cred,omitempty"`

	// from is the remote address of the request this session was validated
	// for (never stored), so the audit names where an action came from.
	from string
}

// From is the remote address of the request the session was validated for, or
// the sign-in address for a session not read from a request.
func (s Session) From() string {
	if s.from != "" {
		return s.from
	}
	return s.Addr
}

// Attribution is who did something and from where, for the audit log:
// operator, session, sign-in method, remote address, listener and transport.
func (s Session) Attribution() map[string]any {
	return map[string]any{"operator": s.Operator, "session": s.ID, "method": string(s.EffectiveMethod()),
		"addr": s.From(), "listener": string(s.Listener), "transport": string(sessTransport(s))}
}

// EffectiveMethod is the session's sign-in method, defaulting to passkey for a
// session that predates the field.
func (s Session) EffectiveMethod() Method {
	if s.Method == "" {
		return MethodPasskey
	}
	return s.Method
}

// token is the opaque secret behind a cookie value. The Session.ID is the
// server-side key; the cookie carries ID.secret and the stored hash verifies it,
// so a leaked session LIST can't be turned into a cookie.
// (see session.go)

// Purpose names what a step-up assertion authorizes. A step-up token is bound to
// {session, purpose, digest} and is single-use (design §6.5).
type Purpose string

const (
	PurposeApprove        Purpose = "approve"      // approve/deny a bot proposal (digest = the approval digest)
	PurposeConfigApply    Purpose = "config.apply" // a config apply/restore that WIDENS
	PurposeSecretWrite    Purpose = "secret.write" // store or rotate a secret
	PurposeBrowserTake    Purpose = "browser.takeover"
	PurposeBrowserWrite   Purpose = "browser.write"    // a wallet change that WIDENS (new profile/card, a binding, a site, a shop, a higher limit, an unfreeze)
	PurposePurchase       Purpose = "browser.purchase" // approve/deny a purchase (digest = the purchase decision digest)
	PurposeUpdateNow      Purpose = "update.apply"
	PurposeRunReplay      Purpose = "run.replay"    // a forced retry: re-runs steps that already succeeded
	PurposePackChange     Purpose = "pack.change"   // pack/plugin add/remove
	PurposeEnrollSecond   Purpose = "enroll.second" // enrol another passkey
	PurposeKnowledgePromo Purpose = "knowledge.promote"
	// PurposeCredential adds or removes a sign-in credential while signed in: a
	// password, a TOTP authenticator, a passkey (digest = CredentialDigest(op, action)).
	PurposeCredential Purpose = "credential.change"
)

// EnrollTicket is a one-time enrollment grant minted by `conductor dashboard
// enroll` over the control socket (design §6.3). The secret lives only in the
// URL fragment the CLI prints; the daemon stores its hash. One live ticket per
// operator.
type EnrollTicket struct {
	Operator    string    `json:"operator"`
	SecretHash  string    `json:"secret_hash"`  // sha256 of the 256-bit secret
	PairingCode string    `json:"pairing_code"` // "417-209"
	Created     time.Time `json:"created"`
	Expires     time.Time `json:"expires"`
	CodeTries   int       `json:"code_tries"` // pairing-code failures; 3 burns the ticket
	Voided      bool      `json:"voided"`     // cancelled by en-cancel / consumed
}
