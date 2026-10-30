package auth

import (
	"strings"
	"context"
	"fmt"
	"time"
)

// ResetPayload is the canonical text `conductor dashboard reset` signs with the
// operator's SSH key, in the enroll namespace with a reset action so it can only
// authorize a reset (design §6.3).
func ResetPayload(operator string, issued time.Time) []byte {
	return []byte(fmt.Sprintf("conductor dashboard reset v1\noperator: %s\nissued: %s\n", operator, issued.UTC().Format(time.RFC3339)))
}

// AuthorizeReset verifies an operator's SSH signature over a ResetPayload before
// a reset proceeds (the same SSH trust root enrollment uses).
func (m *Manager) AuthorizeReset(ctx context.Context, operator string, issued time.Time, sig string) error {
	pub, ok := m.operatorPubKey(operator)
	if !ok {
		return fmt.Errorf("unknown operator %q (or it has no ssh_key)", operator)
	}
	if d := m.now().Sub(issued); d < -enrollSkew || d > enrollSkew {
		return fmt.Errorf("reset request is stale (issued %s)", issued.UTC().Format(time.RFC3339))
	}
	return m.deps.VerifySSH(ctx, operator, pub, ResetPayload(operator, issued), sig)
}

// ResetOperator is `conductor dashboard reset`: it removes an operator's
// passkeys, password, TOTP authenticator, sessions and any live enrollment
// ticket, so first enrollment applies again. It is the recovery path for a lost
// authenticator (there are no recovery codes). It audits and fires
// conductor.dashboard_reset (design §6.3).
func (m *Manager) ResetOperator(operator string) ResetResult {
	var out ResetResult
	out.Passkeys = m.deps.Store.DeleteCredentialsFor(operator)
	_ = m.deps.Store.UpdateOperatorSecret(operator, func(o *OperatorSecret) {
		out.Password, out.TOTP = o.PasswordHash != "", o.TOTPSecret != ""
		*o = OperatorSecret{Operator: operator}
	})
	for _, s := range m.deps.Store.SessionsFor(operator) {
		_ = m.deps.Store.DeleteSession(s.ID)
		out.Sessions++
	}
	_, out.Ticket = m.deps.Store.Ticket(operator)
	_ = m.deps.Store.DeleteTicket(operator)
	m.audit(map[string]any{"event": "dashboard_reset", "operator": operator, "passkeys": out.Passkeys, "sessions": out.Sessions,
		"password": out.Password, "totp": out.TOTP, "ticket": out.Ticket})
	if m.deps.Lifecycle != nil {
		m.deps.Lifecycle("conductor.dashboard_reset", operator, out.String())
	}
	return out
}

// ResetResult is what a reset removed.
type ResetResult struct {
	Passkeys int  `json:"passkeys"`
	Sessions int  `json:"sessions"`
	Password bool `json:"password"`
	TOTP     bool `json:"totp"`
	Ticket   bool `json:"ticket"` // a live enrolment link
}

// String is the reset in words: "2 passkey(s), 1 session(s), the password and
// the authenticator removed".
func (r ResetResult) String() string {
	parts := []string{fmt.Sprintf("%d passkey(s)", r.Passkeys), fmt.Sprintf("%d session(s)", r.Sessions)}
	if r.Password {
		parts = append(parts, "the password")
	}
	if r.TOTP {
		parts = append(parts, "the authenticator")
	}
	if r.Ticket {
		parts = append(parts, "the enrolment link")
	}
	last := len(parts) - 1
	return strings.Join(parts[:last], ", ") + " and " + parts[last] + " removed"
}

// StatusLine is `conductor dashboard status`: the listeners and, per operator,
// how many passkeys and sessions exist, plus whether a live enrollment ticket is
// outstanding.
type StatusLine struct {
	Listeners []ListenerFact      `json:"listeners"`
	Operators map[string]OpStatus `json:"operators"`
}

// OpStatus is one operator's enrollment/session counts.
type OpStatus struct {
	Passkeys   int  `json:"passkeys"`
	Sessions   int  `json:"sessions"`
	LiveTicket bool `json:"live_ticket"`
	Password   bool `json:"password"`
	TOTP       bool `json:"totp"`
}

// Status reports the dashboard's auth state for every configured operator.
func (m *Manager) Status() StatusLine {
	out := StatusLine{Listeners: m.listenerFacts(), Operators: map[string]OpStatus{}}
	if m.deps.Operators != nil {
		for op := range m.deps.Operators() {
			_, live := m.deps.Store.Ticket(op)
			rec, _ := m.deps.Store.OperatorSecret(op)
			out.Operators[op] = OpStatus{
				Passkeys:   len(m.deps.Store.CredentialsFor(op)),
				Sessions:   len(m.deps.Store.SessionsFor(op)),
				LiveTicket: live,
				Password:   rec.PasswordHash != "",
				TOTP:       rec.TOTPSecret != "",
			}
		}
	}
	return out
}

// EnrollURL builds the one-time localhost enrollment URL the CLI prints: the
// secret lives in the fragment so it never reaches a log (design §6.3).
func EnrollURL(port int, secret string) string {
	return fmt.Sprintf("http://localhost:%d/enroll#t=%s", port, secret)
}
