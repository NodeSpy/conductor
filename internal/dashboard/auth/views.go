package auth

import (
	"sort"
	"time"
)

// PasskeyView is the Access screen's shape for one passkey (never the public key
// bytes: the screen shows provenance, not material).
type PasskeyView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RPID        string `json:"rp_id"`
	Listener    string `json:"listener"`
	DeviceBound bool   `json:"device_bound"`
	Created     string `json:"created"`
	LastUsed    string `json:"last_used"`
	Uses        int    `json:"uses"`
	Locked      bool   `json:"locked"`
}

func passkeyView(c *Credential) PasskeyView {
	return PasskeyView{
		ID: c.ID, Name: c.Name, RPID: c.RPID, Listener: string(c.Listener),
		DeviceBound: c.DeviceBound, Created: tsOrEmpty(c.Created), LastUsed: tsOrEmpty(c.LastUsed),
		Uses: c.Uses, Locked: c.Locked,
	}
}

func (m *Manager) passkeyList(operator string) []PasskeyView {
	out := []PasskeyView{}
	for _, c := range m.deps.Store.CredentialsFor(operator) {
		out = append(out, passkeyView(c))
	}
	return out
}

// SessionView is the Access screen's shape for one session.
type SessionView struct {
	ID       string `json:"id"`
	Device   string `json:"device"`
	Addr     string `json:"addr"`
	Listener string `json:"listener"`
	Created  string `json:"created"`
	LastSeen string `json:"last_seen"`
	Current  bool   `json:"current"`
}

func (m *Manager) sessionList(current Session) []SessionView {
	out := []SessionView{}
	for _, s := range m.deps.Store.SessionsFor(current.Operator) {
		out = append(out, SessionView{
			ID: s.ID, Device: s.Device, Addr: s.Addr, Listener: string(s.Listener),
			Created: tsOrEmpty(s.Created), LastSeen: tsOrEmpty(s.LastSeen),
			Current: s.ID == current.ID,
		})
	}
	return out
}

// ListenerFact is one bound listener on the Access screen.
type ListenerFact struct {
	Listener string   `json:"listener"`
	RPID     string   `json:"rp_id"`
	Origins  []string `json:"origins"`
}

func (m *Manager) listenerFacts() []ListenerFact {
	out := []ListenerFact{}
	for _, rp := range m.rps {
		out = append(out, ListenerFact{Listener: string(rp.Listener), RPID: rp.ID, Origins: rp.Origins})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Listener < out[j].Listener })
	return out
}

// StepupFresh reports whether a session's last step-up is within d (for the UI's
// freshness readout).
func StepupFresh(s Session, d time.Duration, now time.Time) bool {
	return !s.StepupAt.IsZero() && now.Sub(s.StepupAt) <= d
}
