package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	vwa "github.com/descope/virtualwebauthn"
)

// The operator buckets (password and TOTP failures from anywhere) and their
// doubling survive a restart: a reload must not hand a guesser fresh tries.
func TestOperatorLockSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	open := func() *Manager {
		st, err := OpenStore(dir, clk.now)
		if err != nil {
			t.Fatal(err)
		}
		m, err := NewManager(Deps{Store: st, Now: clk.now,
			Operators: func() map[string]string { return map[string]string{"ops": "ssh-ed25519 AAAA ops"} }},
			RP{ID: "localhost", Origins: []string{testOrigin}, Listener: ListenerLocalhost})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := open()
	secret := setTOTP(t, m, "ops")
	_, sess, _ := m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1:1", "")
	for i := 0; i < loginMaxFails; i++ {
		_, _ = m.TOTPStepup(sess, PurposeApprove, "d", "999999")
	}
	m = open() // a restart
	_, sess, _ = m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1:1", "")
	if _, err := m.TOTPStepup(sess, PurposeApprove, "d", codeAt(t, secret, clk.now())); err == nil || !strings.Contains(err.Error(), "operator ops") {
		t.Fatalf("the lock must survive a restart: %v", err)
	}
	// The doubling survives too: after the first lock, five more failures lock
	// for twice as long.
	clk.advance(loginBaseLock + time.Second)
	m = open()
	_, sess, _ = m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1:1", "")
	for i := 0; i < loginMaxFails; i++ {
		_, _ = m.TOTPStepup(sess, PurposeApprove, "d", "999999")
	}
	m = open()
	locked, until := m.lock.Locked(operatorLockKey("ops"))
	if !locked || until.Sub(clk.now()) < 2*loginBaseLock-time.Second {
		t.Fatalf("the second lock must be doubled across restarts: locked=%v until %v", locked, until.Sub(clk.now()))
	}
}

// One address's failures don't lock a different address out of SSH or
// passkeys, even on the same IPv6 /64 or IPv4 /24, and the 429 says which
// bucket refused.
func TestNeighbourCantLockOutSSHOrPasskeys(t *testing.T) {
	h := newMethodsHarness(t, nil)
	for _, c := range []struct{ attacker, operator string }{
		{"[2001:db8:1:2::bad]:1", "[2001:db8:1:2:aaaa:bbbb:cccc:1]:2"},
		{"192.0.2.66:1", "192.0.2.200:2"},
	} {
		for i := 0; i < 2*loginMaxFails; i++ {
			call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.example", c.attacker, onHTTPS, map[string]string{"code": "ZZZZ-ZZZZ"}, nil)
			call(h.m, "POST", "/api/v1/auth/ssh/finish", "box.example", c.attacker, onHTTPS, map[string]string{"challenge_id": "x", "signature": "y"}, nil)
		}
		rr := call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.example", c.attacker, onHTTPS, map[string]string{"code": "ZZZZ-ZZZZ"}, nil)
		if rr.Code != http.StatusTooManyRequests || !strings.Contains(errMessage(rr), "from this address") {
			t.Fatalf("%s: the attacker's own address is locked, and told so: %d %s", c.attacker, rr.Code, errMessage(rr))
		}
		if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", c.operator, onHTTPS, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
			t.Fatalf("%s: a neighbour locked the operator out of SSH: %d %s", c.operator, rr.Code, rr.Body)
		}
		if rr := call(h.m, "POST", "/api/v1/auth/login/begin", "box.example", c.operator, onHTTPS, nil, nil); rr.Code != http.StatusOK {
			t.Fatalf("%s: a neighbour locked the operator out of passkeys: %d %s", c.operator, rr.Code, rr.Body)
		}
	}
}

// The network bucket only throttles guessing spread over many addresses: 50
// wrong codes from one /64 pause code guessing from it, not SSH sign-in.
func TestNetworkThrottleIsLooseAndCodeOnly(t *testing.T) {
	h := newMethodsHarness(t, nil)
	for i := 0; i < netMaxFails; i++ {
		call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.example", fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1), onHTTPS, map[string]string{"code": "ZZZZ-ZZZZ"}, nil)
	}
	fresh := "[2001:db8:1:2::ffff]:1"
	rr := call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.example", fresh, onHTTPS, map[string]string{"code": "ZZZZ-ZZZZ"}, nil)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(errMessage(rr), "from this network") {
		t.Fatalf("the /64's code guessing must be throttled: %d %s", rr.Code, errMessage(rr))
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", fresh, onHTTPS, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
		t.Fatalf("the network throttle must not stop SSH sign-in: %d %s", rr.Code, rr.Body)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.example", "[2001:db8:1:3::1]:1", onHTTPS, map[string]string{"code": "ZZZZ-ZZZZ"}, nil); rr.Code == http.StatusTooManyRequests {
		t.Fatalf("another /64 is not throttled: %d %s", rr.Code, rr.Body)
	}
	h.clk.advance(loginWindow + time.Second)
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.example", fresh, onHTTPS, map[string]string{"code": "ZZZZ-ZZZZ"}, nil); rr.Code == http.StatusTooManyRequests {
		t.Fatalf("the throttle lifts when the window moves on (it never locks): %d %s", rr.Code, rr.Body)
	}
}

// On loopback every local process shares an address, so SSH and passkey
// failures lock nothing there; guessing the operator's password is still
// locked by the operator bucket.
func TestLoopbackFailuresNeverLockSSH(t *testing.T) {
	h := newMethodsHarness(t, nil)
	for i := 0; i < 3*loginMaxFails; i++ {
		call(h.m, "POST", "/api/v1/auth/ssh/finish", "localhost:7788", "127.0.0.1:1", onLocalhost, map[string]string{"challenge_id": "x", "signature": "y"}, nil)
		call(h.m, "POST", "/api/v1/auth/login/finish", "localhost:7788", "[::1]:1", onLocalhost, map[string]any{"ceremony_id": "x", "response": map[string]any{}}, nil)
	}
	for _, addr := range []string{"127.0.0.1:2", "[::1]:2"} {
		if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "localhost:7788", addr, onLocalhost, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
			t.Fatalf("%s: local failures locked SSH sign-in: %d %s", addr, rr.Code, rr.Body)
		}
	}
	_ = h.m.SetPassword("ops", "a long enough password")
	for i := 0; i < loginMaxFails; i++ {
		call(h.m, "POST", "/api/v1/auth/password/login", "localhost:7788", "127.0.0.1:3", onLocalhost, map[string]string{"operator": "ops", "password": "wrong guess xx"}, nil)
	}
	rr := call(h.m, "POST", "/api/v1/auth/password/login", "localhost:7788", "127.0.0.1:3", onLocalhost, map[string]string{"operator": "ops", "password": "a long enough password"}, nil)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(errMessage(rr), "operator ops") || !strings.Contains(errMessage(rr), "SSH keys and passkeys still work") {
		t.Fatalf("password guessing on loopback is still locked per operator: %d %s", rr.Code, errMessage(rr))
	}
}

// A LAN neighbour holding many addresses can't fill the pending SSH table for
// the operator: 3 per address, 256 per network.
func TestPendingSSHNeighbourCantFillTheNetwork(t *testing.T) {
	h := newMethodsHarness(t, nil)
	accepted := 0
	for ip := 1; ip <= 7; ip++ {
		for i := 0; i < 5; i++ {
			if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", fmt.Sprintf("192.0.2.%d:1", ip), onHTTPS, map[string]string{"operator": "x"}, nil); rr.Code == http.StatusOK {
				accepted++
			}
		}
	}
	if accepted != 7*maxSSHPendingClient {
		t.Fatalf("%d begins accepted, want %d (per-address cap)", accepted, 7*maxSSHPendingClient)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", "192.0.2.200:1", onHTTPS, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
		t.Fatalf("the operator on the same /24 must still begin: %d %s", rr.Code, rr.Body)
	}
}

// The enrolment link is single use: the first redeem spends it.
func TestEnrollLinkIsSingleUse(t *testing.T) {
	h := newHarness(t)
	secret, code, _, err := h.m.MintTicket(t.Context(), "ops", h.clk.now(), "sig")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.m.RedeemTicket(secret, code); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, _, _, err := h.m.RedeemTicket(secret, code); err == nil {
		t.Fatal("a second redeem of the same link must fail")
	}
	if _, live := h.m.deps.Store.Ticket("ops"); live {
		t.Fatal("the redeemed link must not stay live")
	}
}

// Neighbours on one network who exhaust its bucket with wrong codes and
// passwords from many addresses throttle guessing from that network, and
// nothing else: a different address on it still begins and finishes SSH
// sign-in and passkey sign-in. Signatures and passkey assertions can't be
// guessed, so they never count against the network.
func TestNetworkBucketNeverBlocksSSHOrPasskeys(t *testing.T) {
	type net struct {
		name     string
		neighbor func(i int) string
		operator string
	}
	nets := []net{
		{"IPv6 /64", func(i int) string { return fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1) }, "[2001:db8:1:2:aaaa:bbbb:cccc:1]:2"},
		{"IPv4 /24", func(i int) string { return fmt.Sprintf("192.0.2.%d:1", i+1) }, "192.0.2.200:2"},
	}
	// exhaust sends wrong codes and wrong passwords from 2*netMaxFails
	// addresses on the network, then checks the network is throttled.
	exhaust := func(t *testing.T, m *Manager, host string, c ConnInfo, n net) {
		t.Helper()
		for i := 0; i < netMaxFails; i++ {
			call(m, "POST", "/api/v1/auth/ssh/pending", host, n.neighbor(i), c, map[string]string{"code": "ZZZZ-ZZZZ"}, nil)
			call(m, "POST", "/api/v1/auth/password/login", host, n.neighbor(netMaxFails+i), c,
				map[string]string{"operator": "ops", "password": "wrong guess xx", "code": "000000"}, nil)
		}
		rr := call(m, "POST", "/api/v1/auth/ssh/pending", host, n.operator, c, map[string]string{"code": "ZZZZ-ZZZZ"}, nil)
		if rr.Code != http.StatusTooManyRequests || !strings.Contains(errMessage(rr), "from this network") {
			t.Fatalf("%s: the network's guessing must be throttled first: %d %s", n.name, rr.Code, errMessage(rr))
		}
	}

	for _, n := range nets {
		t.Run(n.name+" ssh", func(t *testing.T) {
			h := newMethodsHarness(t, nil)
			exhaust(t, h.m, "box.example", onHTTPS, n)
			rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", n.operator, onHTTPS, map[string]string{"operator": "ops"}, nil)
			var v SSHChallengeView
			if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &v) != nil {
				t.Fatalf("ssh/begin: %d %s", rr.Code, rr.Body)
			}
			rr = call(h.m, "POST", "/api/v1/auth/ssh/finish", "box.example", n.operator, onHTTPS,
				map[string]string{"challenge_id": v.ID, "signature": h.key.sign(t, LoginNamespace, v.Text)}, nil)
			if rr.Code != http.StatusOK || len(rr.Result().Cookies()) == 0 {
				t.Fatalf("ssh/finish from a fresh address on the throttled network: %d %s", rr.Code, rr.Body)
			}
		})
		t.Run(n.name+" passkey", func(t *testing.T) {
			h := newHarness(t)
			auth := h.newAuthenticator(vwa.AuthenticatorOptions{})
			cred, _ := h.enroll(auth)
			exhaust(t, h.m, "localhost:7788", onLocalhost, n)
			rr := call(h.m, "POST", "/api/v1/auth/login/begin", "localhost:7788", n.operator, onLocalhost, nil, nil)
			var begun struct {
				ID      string          `json:"ceremony_id"`
				Options json.RawMessage `json:"options"`
			}
			if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &begun) != nil {
				t.Fatalf("login/begin: %d %s", rr.Code, rr.Body)
			}
			parsed, err := vwa.ParseAssertionOptions(string(begun.Options))
			if err != nil {
				t.Fatal(err)
			}
			resp := vwa.CreateAssertionResponse(h.rp, *auth, cred, *parsed)
			rr = call(h.m, "POST", "/api/v1/auth/login/finish", "localhost:7788", n.operator, onLocalhost,
				map[string]any{"ceremony_id": begun.ID, "response": json.RawMessage(resp)}, nil)
			if rr.Code != http.StatusOK || len(rr.Result().Cookies()) == 0 {
				t.Fatalf("login/finish from a fresh address on the throttled network: %d %s", rr.Code, rr.Body)
			}
		})
	}
}
