package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sessionRig is a Manager over a store that a second Manager can reopen, with
// a configurable operator key and session.max.
type sessionRig struct {
	t   *testing.T
	dir string
	clk *clock
	key map[string]string
}

func newSessionRig(t *testing.T) *sessionRig {
	return &sessionRig{t: t, dir: t.TempDir(), clk: &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)},
		key: map[string]string{"ops": "ssh-ed25519 AAAAkeyone ops@box"}}
}

func (g *sessionRig) manager(max time.Duration) *Manager {
	st, err := OpenStore(g.dir, g.clk.now)
	if err != nil {
		g.t.Fatal(err)
	}
	m, err := NewManager(Deps{Store: st, Now: g.clk.now, SessionMax: max, SessionIdle: time.Hour,
		Operators: func() map[string]string { return g.key }},
		RP{ID: "localhost", Origins: []string{testOrigin}, Listener: ListenerLocalhost})
	if err != nil {
		g.t.Fatal(err)
	}
	return m
}

func validate(m *Manager, ck *http.Cookie) error {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(ck)
	_, err := m.ValidateCookie(WithConn(r, onLocalhost), ListenerLocalhost, "")
	return err
}

// Changing the ssh_key ends the sessions signed in with the old key; changing
// the password or authenticator ends the password sessions, except the one
// that made the change (rebound to the new credential).
func TestSessionsEndWhenTheirCredentialChanges(t *testing.T) {
	g := newSessionRig(t)
	m := g.manager(12 * time.Hour)
	r := WithConn(httptest.NewRequest("POST", "/", nil), onLocalhost)
	sshCk, _, err := m.CreateSessionFor(r, "ops", MethodSSH, "")
	if err != nil || validate(m, sshCk) != nil {
		t.Fatalf("a fresh SSH session: %v %v", err, validate(m, sshCk))
	}
	g.key["ops"] = "ssh-ed25519 AAAAkeytwo ops@box"
	if err := validate(m, sshCk); err == nil {
		t.Fatal("an SSH session must end when the operator's ssh_key changes")
	}
	g.key["ops"] = "ssh-ed25519 AAAAkeyone ops@box"
	if err := validate(m, sshCk); err == nil {
		t.Fatal("an ended session stays ended when the key comes back")
	}

	_ = m.SetPassword("ops", "the first password")
	mine, own, _ := m.CreateSessionFor(r, "ops", MethodPassword, "")
	other, _, _ := m.CreateSessionFor(r, "ops", MethodPassword, "")
	_ = m.SetPassword("ops", "the second password")
	m.rebind(own)
	if err := validate(m, mine); err != nil {
		t.Fatalf("the session that changed the password stays: %v", err)
	}
	if err := validate(m, other); err == nil {
		t.Fatal("another session on the old password must end")
	}
	third, _, _ := m.CreateSessionFor(r, "ops", MethodPassword, "")
	setTOTP(t, m, "ops")
	if err := validate(m, third); err == nil {
		t.Fatal("a password session must end when the authenticator changes")
	}

	delete(g.key, "ops")
	if err := validate(m, mine); err == nil {
		t.Fatal("a session must end when its operator is removed")
	}
}

// A shorter session.max applies to sessions that already exist, and the list
// shows live sessions only, each with when it ends.
func TestShorterMaxAndLiveList(t *testing.T) {
	g := newSessionRig(t)
	m := g.manager(time.Hour)
	r := WithConn(httptest.NewRequest("POST", "/", nil), onLocalhost)
	ck, _, _ := m.CreateSessionFor(r, "ops", MethodSSH, "")
	_, _, _ = m.CreateSessionFor(r, "ops", MethodSSH, "") // ends under the 10m max below
	g.clk.advance(2 * time.Minute)
	m = g.manager(time.Minute) // restarted with session.max: 1m
	if err := validate(m, ck); err == nil {
		t.Fatal("a session older than the new max must end")
	}
	m = g.manager(time.Hour)
	_, cur, _ := m.CreateSessionFor(r, "ops", MethodSSH, "")
	g.clk.advance(9 * time.Minute) // the 12:00 session is past a 10m max, this one isn't
	m = g.manager(10 * time.Minute)
	list := m.sessionList(cur)
	if len(list) != 1 || list[0].ID != cur.ID || !list[0].Current {
		t.Fatalf("only the live session is listed: %+v", list)
	}
	if want := cur.Created.Add(10 * time.Minute).UTC().Format(time.RFC3339); list[0].Expires != want {
		t.Fatalf("expires = %s, want %s (the new max)", list[0].Expires, want)
	}
	if n := m.Status().Operators["ops"].Sessions; n != 1 {
		t.Fatalf("status counts live sessions only: %d", n)
	}
}

// Through the handlers: a password change made by one session keeps that
// session and ends the operator's other password sessions.
func TestPasswordChangeHandlerEndsOtherSessions(t *testing.T) {
	g := newSessionRig(t)
	m := g.manager(12 * time.Hour)
	_ = m.SetPassword("ops", "the first password")
	secret := setTOTP(t, m, "ops")
	r := WithConn(httptest.NewRequest("POST", "/", nil), onLocalhost)
	mine, sess, _ := m.CreateSessionFor(r, "ops", MethodPassword, "")
	other, _, _ := m.CreateSessionFor(r, "ops", MethodPassword, "")
	tok, err := m.TOTPStepup(sess, PurposeCredential, CredentialDigest("ops", "password.set"), codeAt(t, secret, g.clk.now()))
	if err != nil {
		t.Fatal(err)
	}
	rr := call(m, "POST", "/api/v1/auth/password", "localhost:7788", "127.0.0.1:1", onLocalhost, map[string]string{"password": "the second password"},
		map[string]string{"Cookie": mine.Name + "=" + mine.Value, CSRFHeader: sess.CSRF, StepupHeader: tok})
	if rr.Code != http.StatusOK {
		t.Fatalf("password set: %d %s", rr.Code, rr.Body)
	}
	if err := validate(m, mine); err != nil {
		t.Fatalf("the session that changed the password stays: %v", err)
	}
	if err := validate(m, other); err == nil {
		t.Fatal("another session on the old password must end")
	}
}
