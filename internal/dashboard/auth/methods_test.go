package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixtures: a real SSH key per operator, requests on a chosen connection ---

type sshKey struct{ priv, pub string }

func newSSHKey(t *testing.T) sshKey {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "ops@test", "-f", priv).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return sshKey{priv: priv, pub: strings.TrimSpace(string(pub))}
}

func (k sshKey) sign(t *testing.T, ns, text string) string {
	t.Helper()
	sig, err := SignSSH(context.Background(), k.priv, ns, []byte(text))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

// methodsHarness is a Manager whose operator "ops" has a real SSH key, verified
// with the real ssh-keygen (so namespace binding is the real one).
type methodsHarness struct {
	*harness
	key sshKey
}

func newMethodsHarness(t *testing.T, pol *Policy) *methodsHarness {
	t.Helper()
	key := newSSHKey(t)
	st, err := OpenStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
	st.now = clk.now
	m, err := NewManager(Deps{
		Store:          st,
		Operators:      func() map[string]string { return map[string]string{"ops": key.pub, "other": key.pub} },
		VerifySSH:      func(ctx context.Context, operator, pubKey string, payload []byte, sig string) error { return nil },
		Policy:         pol,
		Now:            clk.now,
		DescribeStepup: describeAny,
	},
		RP{ID: "localhost", Origins: []string{testOrigin}, Listener: ListenerLocalhost},
		RP{ID: "box.example", Origins: []string{"https://box.example:7790"}, Listener: Listener("bind:0.0.0.0:7790")},
	)
	if err != nil {
		t.Fatal(err)
	}
	return &methodsHarness{harness: &harness{t: t, m: m, clk: clk}, key: key}
}

// req builds a request as a listener wrapper would tag it.
func req(method, path, host string, c ConnInfo, body any) *http.Request {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, strings.NewReader(string(b)))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = host
	r.RemoteAddr = "192.0.2.10:5555"
	if method != http.MethodGet {
		r.Header.Set("Content-Type", "application/json") // as the SPA sends it
	}
	return WithConn(r, c)
}

var (
	onLocalhost = ConnInfo{Listener: ListenerLocalhost}
	onHTTPS     = ConnInfo{Listener: Listener("bind:0.0.0.0:7790"), Transport: TransportHTTPS}
	onPlainHTTP = ConnInfo{Listener: Listener("bind:0.0.0.0:7791"), Transport: TransportHTTP}
)

func localReq() *http.Request { return req("POST", "/x", "localhost:7788", onLocalhost, nil) }

// --- argon2id ---

func TestArgon2RoundTrip(t *testing.T) {
	h := newHarness(t)
	phc, err := h.m.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC string %q", phc)
	}
	if !h.m.verifyPassword(phc, "correct horse battery") {
		t.Fatal("the right password must verify")
	}
	if h.m.verifyPassword(phc, "correct horse batterY") {
		t.Fatal("a wrong password must not verify")
	}
	again, _ := h.m.HashPassword("correct horse battery")
	if again == phc {
		t.Fatal("two hashes of one password must differ (fresh salt)")
	}
	if h.m.verifyPassword("", "anything at all") || h.m.verifyPassword(dummyPHC, "") {
		t.Fatal("no stored hash (the dummy) must never verify")
	}
	if _, err := h.m.HashPassword("short"); err == nil {
		t.Fatal("a password under the minimum length must be refused")
	}
}

// --- TOTP ---

// RFC 6238 Appendix B, SHA-1 rows (the 8-digit values, last 6 digits).
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, v := range []struct {
		unix int64
		want string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		got, err := TOTPCode(secret, v.unix/30)
		if err != nil || got != v.want {
			t.Errorf("T=%d: got %q (%v), want %q", v.unix, got, err, v.want)
		}
	}
}

func setTOTP(t *testing.T, m *Manager, op string) string {
	t.Helper()
	secret := b32.EncodeToString([]byte("an-operator-secret!!"))
	if err := m.deps.Store.UpdateOperatorSecret(op, func(o *OperatorSecret) { o.TOTPSecret = secret }); err != nil {
		t.Fatal(err)
	}
	return secret
}

func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	c, err := TOTPCode(secret, totpStep(at))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTOTPSkewAndReplay(t *testing.T) {
	h := newHarness(t)
	secret := setTOTP(t, h.m, "ops")
	now := h.clk.now()
	for _, d := range []time.Duration{-90 * time.Second, -60 * time.Second, 60 * time.Second, 90 * time.Second} {
		if err := h.m.checkTOTP("ops", codeAt(t, secret, now.Add(d))); err == nil {
			t.Fatalf("a code %v away must be outside the ±1 step window", d)
		}
	}
	if err := h.m.checkTOTP("ops", codeAt(t, secret, now.Add(-30*time.Second))); err != nil {
		t.Fatalf("the previous step is within skew: %v", err)
	}
	cur := codeAt(t, secret, now)
	if err := h.m.checkTOTP("ops", cur); err != nil {
		t.Fatalf("the current code must verify: %v", err)
	}
	if err := h.m.checkTOTP("ops", cur); err == nil {
		t.Fatal("the same code twice is a replay")
	}
	if err := h.m.checkTOTP("ops", codeAt(t, secret, now.Add(-30*time.Second))); err == nil {
		t.Fatal("a code for an older step than the last accepted is a replay")
	}
	if err := h.m.checkTOTP("ops", codeAt(t, secret, now.Add(30*time.Second))); err != nil {
		t.Fatalf("the next step (+1 skew) is newer and must verify: %v", err)
	}
	if err := h.m.checkTOTP("ops", "12 34 5"); err == nil {
		t.Fatal("a malformed code must fail")
	}
}

func TestTOTPEnrollConfirm(t *testing.T) {
	h := newHarness(t)
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	en, err := h.m.BeginTOTP(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(en.URI, "otpauth://totp/conductor:ops?") || !strings.Contains(en.URI, "secret="+en.Secret) || !strings.HasPrefix(en.QR, "data:image/png;base64,") {
		t.Fatalf("bad enrollment presentation: %+v", en.URI)
	}
	if err := h.m.ConfirmTOTP(sess, "000000"); err == nil && codeAt(t, en.Secret, h.clk.now()) != "000000" {
		t.Fatal("a wrong confirming code must not activate the authenticator")
	}
	code := codeAt(t, en.Secret, h.clk.now())
	if err := h.m.ConfirmTOTP(sess, code); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := h.m.checkTOTP("ops", code); err == nil {
		t.Fatal("the confirming code must be spent, not reusable to sign in")
	}
}

// Replacing an authenticator: the old one keeps working until the new one is
// confirmed with a code from it, then only the new one does.
func TestTOTPReplaceKeepsOldUntilConfirmed(t *testing.T) {
	h := newHarness(t)
	old := setTOTP(t, h.m, "ops")
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	en, err := h.m.BeginTOTP(sess)
	if err != nil {
		t.Fatal(err)
	}
	h.clk.advance(30 * time.Second)
	if err := h.m.checkTOTP("ops", codeAt(t, old, h.clk.now())); err != nil {
		t.Fatalf("the old authenticator must work while the new one is unconfirmed: %v", err)
	}
	if err := h.m.ConfirmTOTP(sess, codeAt(t, old, h.clk.now())); err == nil && codeAt(t, old, h.clk.now()) != codeAt(t, en.Secret, h.clk.now()) {
		t.Fatal("a code from the old authenticator must not confirm the new one")
	}
	h.clk.advance(30 * time.Second)
	if err := h.m.ConfirmTOTP(sess, codeAt(t, en.Secret, h.clk.now())); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	h.clk.advance(30 * time.Second)
	if err := h.m.checkTOTP("ops", codeAt(t, old, h.clk.now())); err == nil {
		t.Fatal("after confirming, the old authenticator must stop working")
	}
	if err := h.m.checkTOTP("ops", codeAt(t, en.Secret, h.clk.now())); err != nil {
		t.Fatalf("the new authenticator must work: %v", err)
	}
}

// --- password sign-in policy ---

func TestPasswordOnlyRejectedByDefault(t *testing.T) {
	h := newHarness(t)
	if err := h.m.SetPassword("ops", "a long enough password"); err != nil {
		t.Fatal(err)
	}
	if err := h.m.PasswordLogin("ops", "a long enough password", ""); err == nil {
		t.Fatal("by default a password without an authenticator must not sign in")
	}
	secret := setTOTP(t, h.m, "ops")
	if err := h.m.PasswordLogin("ops", "a long enough password", ""); err == nil {
		t.Fatal("a password without its code must not sign in")
	}
	if err := h.m.PasswordLogin("ops", "wrong password here", codeAt(t, secret, h.clk.now())); err == nil {
		t.Fatal("a right code with a wrong password must not sign in")
	}
	h.clk.advance(30 * time.Second) // the failed attempt above didn't spend a step, but keep steps distinct
	if err := h.m.PasswordLogin("ops", "a long enough password", codeAt(t, secret, h.clk.now())); err != nil {
		t.Fatalf("password + code must sign in: %v", err)
	}
	if err := h.m.PasswordLogin("nobody", "a long enough password", codeAt(t, secret, h.clk.now())); err == nil {
		t.Fatal("an unknown operator must not sign in")
	}
}

func TestPasswordOnlyKnob(t *testing.T) {
	pol := DefaultPolicy()
	pol.PasswordRequiresTOTP = false
	h := newHarness(t)
	h.m.deps.Policy = &pol
	_ = h.m.SetPassword("ops", "a long enough password")
	if err := h.m.PasswordLogin("ops", "a long enough password", ""); err != nil {
		t.Fatalf("with password_requires_totp: false a password alone signs in: %v", err)
	}
	// An operator who did enrol an authenticator still has to use it.
	setTOTP(t, h.m, "ops")
	if err := h.m.PasswordLogin("ops", "a long enough password", ""); err == nil {
		t.Fatal("with an authenticator enrolled the code is still required")
	}
	// A policy that drops password from methods refuses it outright.
	pol.Methods = []Method{MethodPasskey, MethodSSH}
	if err := h.m.PasswordLogin("ops", "a long enough password", ""); err == nil {
		t.Fatal("password sign-in must be refused when not in methods")
	}
}

func TestOperatorLockoutForGuessableSecrets(t *testing.T) {
	h := newHarness(t)
	_ = h.m.SetPassword("ops", "a long enough password")
	secret := setTOTP(t, h.m, "ops")
	for i := 0; i < loginMaxFails; i++ {
		_ = h.m.PasswordLogin("ops", "guess number "+string(rune('a'+i))+"xxxx", "000000")
	}
	if err := h.m.PasswordLogin("ops", "a long enough password", codeAt(t, secret, h.clk.now())); !errors.As(err, new(lockedError)) || !strings.Contains(err.Error(), "operator ops") {
		t.Fatalf("after %d failures the operator's password sign-in is locked, got %v", loginMaxFails, err)
	}
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	for i := 0; i < loginMaxFails; i++ {
		_, _ = h.m.TOTPStepup(sess, PurposeApprove, "d", "999999")
	}
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", codeAt(t, secret, h.clk.now())); err == nil {
		t.Fatal("TOTP step-up must be locked after repeated wrong codes")
	}
}

// --- step-up by each method: bound to session, purpose and digest; single use ---

func assertStepupBinding(t *testing.T, m *Manager, sess Session, mint func(p Purpose, d string) string) {
	t.Helper()
	digest := DigestOf([]byte("the request"))
	tok := mint(PurposeApprove, digest)
	if _, err := m.RequireStepup(sess, PurposeConfigApply, digest, tok); err == nil {
		t.Fatal("a token for one purpose must not authorize another")
	}
	tok = mint(PurposeApprove, digest)
	if _, err := m.RequireStepup(sess, PurposeApprove, DigestOf([]byte("another request")), tok); err == nil {
		t.Fatal("a token for one digest must not authorize another")
	}
	tok = mint(PurposeApprove, digest)
	other := sess
	other.ID = "someone-else"
	if _, err := m.RequireStepup(other, PurposeApprove, digest, tok); err == nil {
		t.Fatal("a token must not work for another session")
	}
	tok = mint(PurposeApprove, digest)
	if _, err := m.RequireStepup(sess, PurposeApprove, digest, tok); err != nil {
		t.Fatalf("the matching token must be accepted: %v", err)
	}
	if _, err := m.RequireStepup(sess, PurposeApprove, digest, tok); err == nil {
		t.Fatal("a token is single-use")
	}
}

func TestStepupByTOTP(t *testing.T) {
	h := newHarness(t)
	secret := setTOTP(t, h.m, "ops")
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	assertStepupBinding(t, h.m, sess, func(p Purpose, d string) string {
		h.clk.advance(30 * time.Second) // each mint uses a fresh step
		tok, err := h.m.TOTPStepup(sess, p, d, codeAt(t, secret, h.clk.now()))
		if err != nil {
			t.Fatalf("totp step-up: %v", err)
		}
		return tok
	})
	ev := func() StepupEvidence {
		h.clk.advance(30 * time.Second)
		tok, _ := h.m.TOTPStepup(sess, PurposeApprove, "x", codeAt(t, secret, h.clk.now()))
		ev, _ := h.m.RequireStepup(sess, PurposeApprove, "x", tok)
		return ev
	}()
	if ev.Method != MethodTOTP {
		t.Fatalf("the evidence must record the method, got %q", ev.Method)
	}
}

func TestStepupBySSH(t *testing.T) {
	h := newMethodsHarness(t, nil)
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	assertStepupBinding(t, h.m, sess, func(p Purpose, d string) string {
		v, err := h.m.BeginSSHStepup(localReq(), sess, p, d)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(v.Text, "purpose="+string(p)) || !strings.Contains(v.Text, "digest="+d) {
			t.Fatalf("the step-up challenge must commit to purpose and digest: %q", v.Text)
		}
		tok, err := h.m.FinishSSHStepup(context.Background(), localReq(), sess, v.ID, h.key.sign(t, StepupNamespace, v.Text))
		if err != nil {
			t.Fatalf("ssh step-up: %v", err)
		}
		return tok
	})
	// A login signature can't satisfy a step-up (different namespace).
	v, _ := h.m.BeginSSHStepup(localReq(), sess, PurposeApprove, "d")
	if _, err := h.m.FinishSSHStepup(context.Background(), localReq(), sess, v.ID, h.key.sign(t, LoginNamespace, v.Text)); err == nil {
		t.Fatal("a signature in the login namespace must not step up")
	}
	// Cross-device: approve-login against the code, then the page's poll gets
	// the token.
	v, _ = h.m.BeginSSHStepup(localReq(), sess, PurposeApprove, "d2")
	var approved map[string]any
	h.m.deps.Audit = func(e map[string]any) {
		if e["event"] == "dashboard_ssh_approved" {
			approved = e
		}
	}
	approver := req("POST", "/x", "localhost:7788", onLocalhost, nil)
	approver.RemoteAddr = "127.0.0.1:4242"
	if err := h.m.ApproveSSH(context.Background(), approver, v.Code, h.key.sign(t, StepupNamespace, v.Text)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// The audit names who approved from where, and what: the session, the
	// purpose and the digest.
	for k, want := range map[string]string{"approver_addr": "127.0.0.1:4242", "session": sess.ID, "purpose": string(PurposeApprove), "digest": "d2", "operator": "ops"} {
		if approved[k] != want {
			t.Errorf("dashboard_ssh_approved %s = %v, want %q", k, approved[k], want)
		}
	}
	tok, done, err := h.m.PollSSHStepup(localReq(), sess, v.ID)
	if err != nil || !done {
		t.Fatalf("poll after approve: done=%v err=%v", done, err)
	}
	ev, err := h.m.RequireStepup(sess, PurposeApprove, "d2", tok)
	if err != nil || ev.Method != MethodSSH || ev.SSHPayload != v.Text || ev.SSHNamespace != StepupNamespace || ev.SSHSignature == "" {
		t.Fatalf("ssh step-up evidence: %+v err=%v", ev, err)
	}
}

func TestStepupByPasswordNeedsKnob(t *testing.T) {
	h := newHarness(t)
	_ = h.m.SetPassword("ops", "a long enough password")
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	if _, err := h.m.PasswordStepup(sess, PurposeApprove, "d", "a long enough password", ""); err == nil {
		t.Fatal("by default a password re-entry must not satisfy a step-up")
	}
	pol := DefaultPolicy()
	pol.PasswordRequiresTOTP = false
	pol.Stepup = append(pol.Stepup, MethodPassword)
	h.m.deps.Policy = &pol
	if _, err := h.m.PasswordStepup(sess, PurposeApprove, "d", "not the password", ""); err == nil {
		t.Fatal("a wrong password must not step up")
	}
	assertStepupBinding(t, h.m, sess, func(p Purpose, d string) string {
		tok, err := h.m.PasswordStepup(sess, p, d, "a long enough password", "")
		if err != nil {
			t.Fatalf("password step-up: %v", err)
		}
		return tok
	})
}

func TestPasswordStepupLocksOperator(t *testing.T) {
	h := newHarness(t)
	_ = h.m.SetPassword("ops", "a long enough password")
	pol := DefaultPolicy()
	pol.PasswordRequiresTOTP = false
	pol.Stepup = append(pol.Stepup, MethodPassword)
	h.m.deps.Policy = &pol
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	for i := 0; i < loginMaxFails; i++ {
		_, _ = h.m.PasswordStepup(sess, PurposeApprove, "d", "not the password", "")
	}
	if _, err := h.m.PasswordStepup(sess, PurposeApprove, "d", "a long enough password", ""); err == nil {
		t.Fatal("after repeated wrong passwords the right one must wait out the operator lock")
	}
}

func TestStepupListHonoured(t *testing.T) {
	pol := DefaultPolicy()
	pol.Stepup = []Method{MethodSSH}
	h := newMethodsHarness(t, &pol)
	secret := setTOTP(t, h.m, "ops")
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", codeAt(t, secret, h.clk.now())); err == nil {
		t.Fatal("TOTP step-up must be refused when stepup omits totp")
	}
	if _, _, err := h.m.BeginStepup(ListenerLocalhost, sess, PurposeApprove, "d"); err == nil {
		t.Fatal("passkey step-up must be refused when stepup omits passkey")
	}
	got := h.m.StepupMethods(localReq(), sess)
	if len(got) != 1 || got[0] != MethodSSH {
		t.Fatalf("offered step-up methods = %v, want [ssh]", got)
	}
}

// --- SSH sign-in ---

func TestSSHLoginPaste(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ctx := context.Background()
	v, err := h.m.BeginSSHLogin(localReq(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	if v.Namespace != LoginNamespace || !strings.Contains(v.Text, "operator=ops origin=http://localhost:7788 ") {
		t.Fatalf("challenge must name the operator and origin: %+v", v)
	}
	sig := h.key.sign(t, LoginNamespace, v.Text)
	op, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, sig)
	if err != nil || op != "ops" {
		t.Fatalf("finish: op=%q err=%v", op, err)
	}
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, sig); err == nil {
		t.Fatal("a challenge is single-use: replaying the signature must fail")
	}
}

func TestSSHLoginNegative(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ctx := context.Background()
	begin := func() SSHChallengeView {
		v, err := h.m.BeginSSHLogin(localReq(), "ops")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	v := begin()
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, h.key.sign(t, EnrollNamespace, v.Text)); err == nil {
		t.Fatal("a signature in the enroll namespace must not sign in")
	}
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, h.key.sign(t, StepupNamespace, v.Text)); err == nil {
		t.Fatal("a signature in the step-up namespace must not sign in")
	}

	v = begin()
	other := req("POST", "/x", "127.0.0.1:7788", onLocalhost, nil)
	if _, err := h.m.FinishSSHLogin(ctx, other, v.ID, h.key.sign(t, LoginNamespace, v.Text)); err == nil {
		t.Fatal("a challenge begun on one host must not finish on another")
	}
	https := req("POST", "/x", "box.example:7790", onHTTPS, nil)
	if _, err := h.m.FinishSSHLogin(ctx, https, v.ID, h.key.sign(t, LoginNamespace, v.Text)); err == nil {
		t.Fatal("a challenge begun on one listener must not finish on another")
	}

	v = begin()
	h.clk.advance(sshChallengeTTL + time.Second)
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, h.key.sign(t, LoginNamespace, v.Text)); err == nil {
		t.Fatal("an expired challenge must fail")
	}

	v = begin()
	stranger := newSSHKey(t)
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, stranger.sign(t, LoginNamespace, v.Text)); err == nil {
		t.Fatal("a signature by a key that isn't the operator's must fail")
	}
	// A signature over a different text (another challenge) doesn't verify.
	v2 := begin()
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v.ID, h.key.sign(t, LoginNamespace, v2.Text)); err == nil {
		t.Fatal("a signature over another challenge's text must fail")
	}

	// An unknown operator still gets a challenge (no enumeration), which never verifies.
	vu, err := h.m.BeginSSHLogin(localReq(), "nobody")
	if err != nil {
		t.Fatalf("unknown operators get a challenge too: %v", err)
	}
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), vu.ID, h.key.sign(t, LoginNamespace, vu.Text)); err == nil {
		t.Fatal("an unknown operator must not sign in")
	}
	if _, err := h.m.BeginSSHLogin(localReq(), "ops'; rm -rf /"); err == nil {
		t.Fatal("an operator name that can't be quoted safely must be refused")
	}
}

func TestSSHLoginDisabledByPolicy(t *testing.T) {
	pol := DefaultPolicy()
	pol.Methods = []Method{MethodPasskey}
	h := newMethodsHarness(t, &pol)
	if _, err := h.m.BeginSSHLogin(localReq(), "ops"); err == nil {
		t.Fatal("SSH sign-in must be refused when not in methods")
	}
}

func TestApproveLoginCode(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ctx := context.Background()
	v, _ := h.m.BeginSSHLogin(localReq(), "ops")
	if len(v.Code) != 9 || v.Code[4] != '-' {
		t.Fatalf("login code shape: %q", v.Code)
	}
	pend, err := h.m.PendingSSH(localReq(), strings.ToLower(v.Code))
	if err != nil || pend.Challenge != v.Text || pend.Operator != "ops" || pend.Kind != "login" {
		t.Fatalf("pending: %+v err=%v", pend, err)
	}
	// Before approval the page keeps waiting.
	if _, done, err := h.m.PollSSHLogin(localReq(), v.ID); done || err != nil {
		t.Fatalf("poll before approve: done=%v err=%v", done, err)
	}
	sig := h.key.sign(t, LoginNamespace, v.Text)
	if err := h.m.ApproveSSH(ctx, localReq(), v.Code, sig); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := h.m.ApproveSSH(ctx, localReq(), v.Code, sig); err == nil {
		t.Fatal("a code is single-use")
	}
	if _, err := h.m.PendingSSH(localReq(), v.Code); err == nil {
		t.Fatal("a spent code no longer shows the request")
	}
	// Only the page that began it (same host) collects the session, once.
	if _, _, err := h.m.PollSSHLogin(req("POST", "/x", "127.0.0.1:7788", onLocalhost, nil), v.ID); err == nil {
		t.Fatal("a poll from another host must not collect the session")
	}
	op, done, err := h.m.PollSSHLogin(localReq(), v.ID)
	if err != nil || !done || op != "ops" {
		t.Fatalf("poll after approve: op=%q done=%v err=%v", op, done, err)
	}
	if _, _, err := h.m.PollSSHLogin(localReq(), v.ID); err == nil {
		t.Fatal("the approved challenge is spent after one poll")
	}
}

func TestApproveLoginCodeBindingExpiryAndTries(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ctx := context.Background()
	a, _ := h.m.BeginSSHLogin(localReq(), "ops")
	b, _ := h.m.BeginSSHLogin(localReq(), "ops")
	// Binding: a's code with a signature over b's text is refused.
	if err := h.m.ApproveSSH(ctx, localReq(), a.Code, h.key.sign(t, LoginNamespace, b.Text)); err == nil {
		t.Fatal("a code must only accept a signature over its own challenge")
	}
	// Tries: the third wrong signature burns the code.
	for i := 0; i < maxSSHCodeTries-1; i++ {
		_ = h.m.ApproveSSH(ctx, localReq(), a.Code, "garbage")
	}
	if err := h.m.ApproveSSH(ctx, localReq(), a.Code, h.key.sign(t, LoginNamespace, a.Text)); err == nil {
		t.Fatal("after three wrong signatures the code is burned")
	}
	// Expiry.
	h.clk.advance(sshChallengeTTL + time.Second)
	if err := h.m.ApproveSSH(ctx, localReq(), b.Code, h.key.sign(t, LoginNamespace, b.Text)); err == nil {
		t.Fatal("an expired code must fail")
	}
}

func TestApproveLoginRateLimited(t *testing.T) {
	h := newMethodsHarness(t, nil)
	mux := h.m.Mux()
	for i := 0; i < loginMaxFails; i++ {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req("POST", "/api/v1/auth/ssh/pending", "localhost:7788", onLocalhost, map[string]string{"code": "ZZZZ-ZZZZ"}))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("guess %d: status %d", i, rr.Code)
		}
	}
	v, _ := h.m.BeginSSHLogin(localReq(), "ops")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req("POST", "/api/v1/auth/ssh/pending", "localhost:7788", onLocalhost, map[string]string{"code": v.Code}))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong codes the client is locked out, got %d", loginMaxFails, rr.Code)
	}
}

func TestSSHPendingCapPerClient(t *testing.T) {
	h := newMethodsHarness(t, nil)
	for i := 0; i < maxSSHPendingClient; i++ {
		if _, err := h.m.BeginSSHLogin(localReq(), "ops"); err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
	}
	if _, err := h.m.BeginSSHLogin(localReq(), "ops"); err == nil {
		t.Fatal("a client can't hold unlimited pending challenges")
	}
}

func TestParseChallengeRefusesOtherPayloads(t *testing.T) {
	if _, err := ParseSSHChallenge(string(EnrollPayload("ops", time.Now()))); err == nil {
		t.Fatal("sign must refuse an enrollment payload")
	}
	p, err := ParseSSHChallenge(SSHChallengeText("login", "ops", "https://h:1", "", "", "n", time.Now()))
	if err != nil || p.Namespace != LoginNamespace || p.Fields["origin"] != "https://h:1" {
		t.Fatalf("parse login: %+v %v", p, err)
	}
}

// --- transport: cookies and passkey offer ---

func TestCookiePerTransport(t *testing.T) {
	h := newMethodsHarness(t, nil)
	plain := req("POST", "/x", "10.0.0.5:7791", onPlainHTTP, nil)
	ck, _, err := h.m.CreateSessionFor(plain, "ops", MethodSSH, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ck.Name, PlainSessionCookie+"_") || ck.Secure || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("plain-HTTP cookie: %+v", ck)
	}
	r := req("GET", "/x", "10.0.0.5:7791", onPlainHTTP, nil)
	r.AddCookie(ck)
	if s, err := h.m.ValidateCookie(r, onPlainHTTP.Listener, ""); err != nil || s.Transport != TransportHTTP || s.Method != MethodSSH {
		t.Fatalf("plain cookie on its transport: %+v %v", s, err)
	}

	secure := req("POST", "/x", "box.example:7790", onHTTPS, nil)
	ck2, _, _ := h.m.CreateSessionFor(secure, "ops", MethodPassword, "")
	if !strings.HasPrefix(ck2.Name, SessionCookie+"_") || !ck2.Secure || !ck2.HttpOnly {
		t.Fatalf("HTTPS cookie: %+v", ck2)
	}
	local, _, _ := h.m.CreateSessionFor(localReq(), "ops", MethodSSH, "")
	if local.Name != SessionCookie || !local.Secure {
		t.Fatalf("localhost cookie: %+v", local)
	}
	// Cookies ignore the port: a bind listener answering on localhost must not
	// overwrite the localhost listener's cookie, nor another bind's.
	loopBind := ConnInfo{Listener: Listener("bind:127.0.0.1:7792"), Transport: TransportLocalhost}
	lb, _, _ := h.m.CreateSessionFor(req("POST", "/x", "localhost:7792", loopBind, nil), "ops", MethodSSH, "")
	if lb.Name == local.Name || lb.Name == ck2.Name {
		t.Fatalf("a bind listener's cookie shares a name: %q (localhost %q, other bind %q)", lb.Name, local.Name, ck2.Name)
	}

	// A session cookie is only read back on its own transport.
	cross := req("GET", "/x", "box.example:7790", onHTTPS, nil)
	cross.AddCookie(&http.Cookie{Name: ck2.Name, Value: ck.Value})
	if _, err := h.m.ValidateCookie(cross, onHTTPS.Listener, ""); err == nil {
		t.Fatal("a plain-HTTP session must not validate on another transport")
	}
}

func TestPasskeyOfferedOnlyOnSecureContextWithRP(t *testing.T) {
	h := newMethodsHarness(t, nil)
	has := func(r *http.Request) bool {
		for _, m := range h.m.SignInMethods(r) {
			if m == MethodPasskey {
				return true
			}
		}
		return false
	}
	if !has(localReq()) {
		t.Fatal("passkey on http://localhost is offered")
	}
	if has(req("GET", "/", "127.0.0.1:7788", onLocalhost, nil)) {
		t.Fatal("passkey on an IP host (no RP) is not offered")
	}
	if !has(req("GET", "/", "box.example:7790", onHTTPS, nil)) {
		t.Fatal("passkey on an HTTPS hostname with an RP is offered")
	}
	if has(req("GET", "/", "box.example:7791", onPlainHTTP, nil)) {
		t.Fatal("passkey on plain HTTP off localhost is never offered")
	}
	rr := httptest.NewRecorder()
	h.m.Mux().ServeHTTP(rr, req("POST", "/api/v1/auth/login/begin", "10.0.0.5:7791", onPlainHTTP, map[string]string{}))
	if rr.Code == http.StatusOK {
		t.Fatal("a passkey ceremony must not begin on plain HTTP")
	}
}

func TestSSHChallengeBindings(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ctx := context.Background()
	plain := func() *http.Request { return req("POST", "/x", "10.0.0.5:7791", onPlainHTTP, nil) }

	// A challenge is bound to the transport it was issued on, not only the
	// listener and host.
	v, err := h.m.BeginSSHLogin(plain(), "ops")
	if err != nil {
		t.Fatal(err)
	}
	other := req("POST", "/x", "10.0.0.5:7791", ConnInfo{Listener: onPlainHTTP.Listener, Transport: TransportHTTPS}, nil)
	if _, err := h.m.FinishSSHLogin(ctx, other, v.ID, h.key.sign(t, LoginNamespace, v.Text)); err == nil {
		t.Fatal("a sign-in challenge must not finish on another transport")
	}

	// A sign-in challenge is not a step-up challenge, nor the reverse.
	_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	login, _ := h.m.BeginSSHLogin(localReq(), "ops")
	if _, err := h.m.FinishSSHStepup(ctx, localReq(), sess, login.ID, h.key.sign(t, LoginNamespace, login.Text)); err == nil {
		t.Fatal("a signed sign-in challenge must not mint a step-up")
	}
	step, _ := h.m.BeginSSHStepup(localReq(), sess, PurposeApprove, "d")
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), step.ID, h.key.sign(t, StepupNamespace, step.Text)); err == nil {
		t.Fatal("a signed step-up challenge must not sign in")
	}

	// A step-up challenge belongs to the session that asked for it.
	_, sess2, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
	step, _ = h.m.BeginSSHStepup(localReq(), sess, PurposeApprove, "d")
	if _, err := h.m.FinishSSHStepup(ctx, localReq(), sess2, step.ID, h.key.sign(t, StepupNamespace, step.Text)); err == nil {
		t.Fatal("another session must not finish this session's step-up challenge")
	}
}

func TestPasswordStepupNeedsBothKnobs(t *testing.T) {
	for name, pol := range map[string]Policy{
		"totp still required": {Methods: []Method{MethodPasskey, MethodPassword}, PasswordRequiresTOTP: true, Stepup: []Method{MethodPassword}},
		"no password sign-in": {Methods: []Method{MethodPasskey}, PasswordRequiresTOTP: false, Stepup: []Method{MethodPassword}},
	} {
		h := newHarness(t)
		_ = h.m.SetPassword("ops", "a long enough password")
		p := pol
		h.m.deps.Policy = &p
		_, sess, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "t", "127.0.0.1", "")
		if _, err := h.m.PasswordStepup(sess, PurposeApprove, "d", "a long enough password", ""); err == nil {
			t.Fatalf("%s: a password must not step up", name)
		}
	}
}

func TestSecureCookieBoundToTransport(t *testing.T) {
	h := newMethodsHarness(t, nil)
	ck, _, err := h.m.CreateSessionFor(req("POST", "/x", "box.example:7790", onHTTPS, nil), "ops", MethodSSH, "")
	if err != nil {
		t.Fatal(err)
	}
	// Same listener and cookie name, another transport: refused.
	r := req("GET", "/x", "box.example:7790", ConnInfo{Listener: onHTTPS.Listener, Transport: TransportLocalhost}, nil)
	r.AddCookie(ck)
	if _, err := h.m.ValidateCookie(r, onHTTPS.Listener, ""); err == nil {
		t.Fatal("an HTTPS session must not validate on another transport")
	}
}
