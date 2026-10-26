package auth

import (
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
// passkeys, sessions and any live enrollment ticket, so first enrollment applies
// again. It audits and fires conductor.dashboard_reset (design §6.3).
func (m *Manager) ResetOperator(operator string) (passkeys, sessions int) {
	passkeys = m.deps.Store.DeleteCredentialsFor(operator)
	for _, s := range m.deps.Store.SessionsFor(operator) {
		_ = m.deps.Store.DeleteSession(s.ID)
		sessions++
	}
	_ = m.deps.Store.DeleteTicket(operator)
	m.audit(map[string]any{"event": "dashboard_reset", "operator": operator, "passkeys": passkeys, "sessions": sessions})
	if m.deps.Lifecycle != nil {
		m.deps.Lifecycle("conductor.dashboard_reset", operator, fmt.Sprintf("%d passkey(s), %d session(s) removed", passkeys, sessions))
	}
	return passkeys, sessions
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
}

// Status reports the dashboard's auth state for every configured operator.
func (m *Manager) Status() StatusLine {
	out := StatusLine{Listeners: m.listenerFacts(), Operators: map[string]OpStatus{}}
	if m.deps.Operators != nil {
		for op := range m.deps.Operators() {
			_, live := m.deps.Store.Ticket(op)
			out.Operators[op] = OpStatus{
				Passkeys:   len(m.deps.Store.CredentialsFor(op)),
				Sessions:   len(m.deps.Store.SessionsFor(op)),
				LiveTicket: live,
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
