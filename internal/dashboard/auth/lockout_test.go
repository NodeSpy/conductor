package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// On loopback any local process can begin sign-ins, so a pending SSH sign-in
// is bound to the browser that began it (its begin cookie): each beginner
// has its own share, and a new begin evicts only that beginner's own oldest.
// No number of begins, by anyone else, for the operator's name or any
// other, evicts or blocks the operator's pending sign-in, and only the
// operator's browser finishes it. Names that aren't configured operators
// behave the same from outside but never hold a configured operator's slot.
func TestLoopbackPendingSSHIsPerBeginner(t *testing.T) {
	h := newMethodsHarness(t, nil)
	begin := func(addr, op, cookie string) (*httptest.ResponseRecorder, SSHChallengeView) {
		var hdr map[string]string
		if cookie != "" {
			hdr = map[string]string{"Cookie": BeginCookie + "=" + cookie}
		}
		rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "localhost:7788", addr, onLocalhost, map[string]string{"operator": op}, hdr)
		var v SSHChallengeView
		_ = json.Unmarshal(rr.Body.Bytes(), &v)
		return rr, v
	}
	beginner := func(rr *httptest.ResponseRecorder) string {
		for _, c := range rr.Result().Cookies() {
			if c.Name == BeginCookie {
				return c.Value
			}
		}
		return ""
	}
	live := func(id string) bool {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		_, ok := h.m.sshCh[id]
		return ok
	}
	finish := func(addr string, v SSHChallengeView, cookie string) *httptest.ResponseRecorder {
		var hdr map[string]string
		if cookie != "" {
			hdr = map[string]string{"Cookie": BeginCookie + "=" + cookie}
		}
		return call(h.m, "POST", "/api/v1/auth/ssh/finish", "localhost:7788", addr, onLocalhost,
			map[string]string{"challenge_id": v.ID, "signature": h.key.sign(t, LoginNamespace, v.Text)}, hdr)
	}

	// The review's repro: the operator's browser begins, then another local
	// process (no cookie, then a cookie of its own) begins for ops again and
	// again. The operator's challenge stays, and finishes.
	rr, mine := begin("127.0.0.1:3", "ops", "")
	opCookie := beginner(rr)
	if rr.Code != http.StatusOK || opCookie == "" {
		t.Fatalf("the operator's begin: %d %s, cookie %q", rr.Code, rr.Body, opCookie)
	}
	rr, _ = begin("127.0.0.1:9", "ops", "")
	attacker := beginner(rr)
	for i := 0; i < 3*maxSSHPendingClient; i++ {
		begin("127.0.0.1:9", "ops", "")
		begin("[::1]:9", "ops", attacker)
	}
	if !live(mine.ID) {
		t.Fatal("another client's begins evicted the operator's pending sign-in")
	}
	// Only the browser that began it finishes it.
	if rr := finish("127.0.0.1:9", mine, attacker); rr.Code != http.StatusUnauthorized {
		t.Fatalf("another browser finished the operator's sign-in: %d %s", rr.Code, rr.Body)
	}
	if rr := finish("127.0.0.1:3", mine, opCookie); rr.Code != http.StatusOK || len(rr.Result().Cookies()) == 0 {
		t.Fatalf("the operator's finish after the flood: %d %s", rr.Code, rr.Body)
	}

	// A beginner's own share: its 4th begin succeeds and its own 1st goes.
	for _, op := range []string{"ops", "nobody9"} {
		var ids []string
		var ck string
		for i := 0; i < maxSSHPendingClient+1; i++ {
			rr, v := begin("127.0.0.1:4", op, ck)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s begin %d: %d %s", op, i, rr.Code, rr.Body)
			}
			if ck == "" {
				ck = beginner(rr)
			}
			ids = append(ids, v.ID)
		}
		if live(ids[0]) || !live(ids[len(ids)-1]) {
			t.Fatalf("%s: a beginner's oldest must give way to its newest", op)
		}
	}

	// Unknown names stay in their own share.
	_, first := begin("127.0.0.1:3", "ops", opCookie)
	for i := 0; i < 2*maxSSHGhosts; i++ {
		begin("127.0.0.1:2", fmt.Sprintf("ghost%d", i), "")
	}
	if !live(first.ID) {
		t.Fatal("unknown names evicted the operator's challenge")
	}
	h.m.mu.Lock()
	ghosts := 0
	for _, c := range h.m.sshCh {
		if c.ghost {
			ghosts++
		}
	}
	h.m.mu.Unlock()
	if ghosts > maxSSHGhosts {
		t.Fatalf("%d ghost challenges held", ghosts)
	}

	// The table is bounded: past its cap a new begin is refused, and every
	// pending sign-in (the operator's too) is kept.
	for i := 0; i < maxSSHPendingLoop; i++ {
		begin("127.0.0.1:5", "ops", "")
	}
	if rr, _ := begin("127.0.0.1:5", "ops", ""); rr.Code != http.StatusBadRequest || !strings.Contains(errMessage(rr), "too many SSH challenges") {
		t.Fatalf("past the loopback cap: %d %s", rr.Code, rr.Body)
	}
	if !live(first.ID) {
		t.Fatal("a full table evicted the operator's challenge")
	}
}

// Operator buckets are for configured operators. Guesses at names that don't
// exist never create a persisted bucket or rewrite the store, and are bounded
// in memory; a locked unknown name is refused in the same words as a locked
// operator. The store is written when a lock is set or cleared, not on every
// failure.
func TestOperatorBucketsAreForConfiguredOperators(t *testing.T) {
	h := newMethodsHarness(t, nil)
	_ = h.m.SetPassword("ops", "a long enough password")
	writes := 0
	inner := h.m.lock.persist
	h.m.lock.persist = func(l map[string]LockRecord) { writes++; inner(l) }
	login := func(addr, op, pw string) *httptest.ResponseRecorder {
		return call(h.m, "POST", "/api/v1/auth/password/login", "localhost:7788", addr, onLocalhost, map[string]string{"operator": op, "password": pw}, nil)
	}
	// A flood of names from rotating loopback networks (the network throttle
	// doesn't bind there).
	for i := 0; i < 200; i++ {
		login(fmt.Sprintf("127.%d.%d.1:1", i/250, i%250), fmt.Sprintf("ghost%04d", i), "wrong guess xx")
	}
	slots := map[string]bool{}
	for i := 0; i < 20000; i++ {
		b := h.m.operatorBucket(fmt.Sprintf("name%05d", i))
		if !strings.HasPrefix(b, ghostLockPrefix) {
			t.Fatalf("an unknown name's bucket: %q", b)
		}
		slots[b] = true
	}
	if len(slots) > ghostLockSlots || h.m.operatorBucket("ops") != operatorLockKey("ops") {
		t.Fatalf("%d slots for unknown names", len(slots))
	}
	if writes != 0 {
		t.Fatalf("guesses at unknown names wrote the store %d times", writes)
	}
	h.m.lock.mu.Lock()
	n := 0
	for k := range h.m.lock.m {
		if strings.HasPrefix(k, "login:op:") {
			t.Errorf("a bucket for an unknown name: %s", k)
		}
		if strings.HasPrefix(k, "login:"+ghostLockPrefix) {
			n++
		}
	}
	h.m.lock.mu.Unlock()
	if n > ghostLockSlots {
		t.Fatalf("%d ghost buckets", n)
	}
	if recs := h.m.deps.Store.Locks(); len(recs) != 0 {
		t.Fatalf("persisted: %v", recs)
	}

	// Four wrong guesses at ops write nothing; the fifth locks and writes once.
	for i := 0; i < loginMaxFails-1; i++ {
		login(fmt.Sprintf("10.9.%d.1:1", i), "ops", "wrong guess xx")
	}
	if writes != 0 {
		t.Fatalf("failures short of a lock wrote the store %d times", writes)
	}
	login("10.9.9.1:1", "ops", "wrong guess xx")
	if writes != 1 {
		t.Fatalf("the lock wrote the store %d times, want 1", writes)
	}

	// The same refusal for a locked unknown name as for the locked operator.
	for i := 0; i < loginMaxFails; i++ {
		login(fmt.Sprintf("10.8.%d.1:1", i), "nobody", "wrong guess xx")
	}
	ghost, real := login("10.7.0.1:1", "nobody", "x"), login("10.7.1.1:1", "ops", "x")
	if ghost.Code != http.StatusTooManyRequests || real.Code != http.StatusTooManyRequests {
		t.Fatalf("locked: %d %d", ghost.Code, real.Code)
	}
	gm := strings.ReplaceAll(errMessage(ghost), "nobody", "NAME")
	rm := strings.ReplaceAll(errMessage(real), "ops", "NAME")
	if gm != rm {
		t.Fatalf("an unknown name's lock reads differently:\n%s\n%s", gm, rm)
	}
}

// SessionLive is what the config editor asks before it reclaims a session's
// staged edits: a session that still signs in is live; an unknown, ended or
// expired one is not.
func TestSessionLive(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ck, _, err := h.m.CreateSessionFor(localReq(), "ops", MethodSSH, "")
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := strings.Cut(ck.Value, ".")
	if !h.m.SessionLive(id) {
		t.Fatal("a fresh session isn't live")
	}
	if h.m.SessionLive("nope") || h.m.SessionLive("") {
		t.Fatal("an unknown session is live")
	}
	h.clk.advance(13 * time.Hour)
	if h.m.SessionLive(id) {
		t.Fatal("an expired session is live")
	}
}
