//go:build linux || darwin

package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// storeRig is a credRig whose credential comes from the login store: the
// password typed (write-only), the username and the code from the
// environment.
func storeRig(t *testing.T) (*credRig, *LoginStore) {
	r := newCredRig(t)
	r.st.Credentials = nil
	s := &LoginStore{Dir: filepath.Join(t.TempDir(), "store")}
	r.m.Store = s
	r.m.LookupEnv = func(k string) (string, bool) {
		switch k {
		case "TEST_USER":
			return browsertest.User, true
		case "TEST_OTP":
			return "otpauth://totp/Test?secret=" + testSeed, true
		}
		return "", false
	}
	if err := s.Put(LoginRecord{Name: "stored", Method: MethodPassword, Origins: []string{"http://localhost:1", r.bound},
		Bindings: []string{"bot:helper"}, LoginURL: r.bound + "/login"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetValue("stored", "password", browsertest.Password); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRef("stored", "username", "${env.TEST_USER}"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRef("stored", "otp", "${env.TEST_OTP}"); err != nil {
		t.Fatal(err)
	}
	return r, s
}

// A stored login signs in a caller it is bound to (here through a bot), on
// one of its origins; refuses a caller it is not bound to and a lookalike
// origin; records every use; and once revoked, the next use is refused.
func TestCredentialStoredLoginBindingAndRevoke(t *testing.T) {
	r, s := storeRig(t)
	bot := r.caller("k-bot", RequesterOperator)
	bot.Identity, bot.Groups = "agent:other@acme/app", []string{"bot:helper"}
	stranger := r.caller("k-stranger", RequesterOperator)
	stranger.Identity = "agent:other@acme/app"

	// No page open: the sign-in still starts in the separate, narrowed
	// browser (found live: it ran in the anonymous one).
	out := r.do(bot, "login", map[string]any{"credential": "stored"})
	if p, _ := out["profile"].(string); !strings.Contains(p, "logged-in browser") {
		t.Fatalf("a sign-in from no page ran in %q", p)
	}
	if got := r.site.SignIns(); len(got) != 1 {
		t.Fatalf("sign-ins: %v (%v)", got, out)
	}
	if !strings.Contains(r.do(bot, "snapshot", nil)["snapshot"].(string), "Signed in") {
		r.do(bot, "open", map[string]any{"url": r.bound + "/account"})
	}
	r.refused(stranger, "login", map[string]any{"credential": "stored"}, "not bound")

	// The lookalike origin: the same server, another origin.
	r.st.Allow = append(r.st.Allow, strings.TrimPrefix(r.alike, "http://"))
	lk := r.caller("k-alike", RequesterOperator)
	lk.Groups = []string{"bot:helper"}
	r.do(lk, "open", map[string]any{"url": r.alike + "/login"})
	snap := r.do(lk, "snapshot", nil)["snapshot"].(string)
	ref := refOf(t, snap, "Password")
	r.refused(lk, "fill-credential", map[string]any{"credential": "stored", "ref": ref, "field": "password"}, "not the credential's")

	// The anonymous browser landed on the credential's own origin (an open
	// redirect on another allowlisted origin): no fill there.
	an := r.caller("k-anon-redirect", RequesterOperator)
	an.Groups = []string{"bot:helper"}
	landed := r.do(an, "open", map[string]any{"url": r.alike + "/out?to=" + r.bound + "/login"})
	if p, _ := landed["profile"].(string); !strings.Contains(p, "anonymous") || !strings.HasPrefix(landed["url"].(string), r.bound) {
		t.Fatalf("want the anonymous browser on the credential's origin: %v", landed)
	}
	snap = r.do(an, "snapshot", nil)["snapshot"].(string)
	r.refused(an, "fill-credential", map[string]any{"credential": "stored", "ref": refOf(t, snap, "Password"), "field": "password"}, "only in the separate")

	// A non-operator request is refused whatever the binding.
	ev := r.caller("k-event", "event")
	ev.Groups = []string{"bot:helper"}
	r.refused(ev, "login", map[string]any{"credential": "stored"}, "not the operator")

	u := s.UsageOf("stored", 0)
	var oks, refused int
	for _, x := range u {
		switch x.Outcome {
		case "ok":
			oks++
		case "refused":
			refused++
		}
	}
	if oks != 1 || refused < 3 {
		t.Fatalf("usage: %+v", u)
	}
	if rec, _, _ := s.Get("stored"); rec.LastUsed == nil {
		t.Fatal("last used")
	}

	if ok, err := s.Remove("stored"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	r.refused(bot, "login", map[string]any{"credential": "stored"}, "no credential")
	r.noLeak()
	for _, x := range s.UsageOf("", 0) {
		b, _ := json.Marshal(x)
		if strings.Contains(string(b), browsertest.Password) {
			t.Fatal("usage carries a value")
		}
	}
}

// Test login: the agents' pipeline, narrowed to one credential, in its own
// browser: ok, failed with why, needs-you — never a value, never a
// screenshot, no cookie kept, its workspace and browser gone after.
func TestCredentialTestLogin(t *testing.T) {
	r, s := storeRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	res := r.m.TestLogin(ctx, "stored")
	if res.Status != TestOK {
		t.Fatalf("test login: %+v", res)
	}
	if n := len(r.site.SignIns()); n != 1 {
		t.Fatalf("the site saw %d sign-ins", n)
	}
	rec, _, _ := s.Get("stored")
	if rec.LastTest == nil || rec.LastTest.Status != TestOK {
		t.Fatalf("last test: %+v", rec.LastTest)
	}
	if len(r.m.Sessions()) != 0 {
		t.Fatal("the test's browser outlived it")
	}
	if ents, _ := os.ReadDir(filepath.Join(r.m.StateDir, "test-login")); len(ents) != 0 {
		t.Fatal("the test's workspace outlived it")
	}
	if jars, _ := r.m.Logins().List(); len(jars) != 0 {
		t.Fatal("a test kept a session")
	}
	b, _ := json.Marshal(res)
	r.shared = append(r.shared, string(b))

	// A wrong password: failed, with why.
	s.SetValue("stored", "password", "wrong-"+browsertest.Password)
	res = r.m.TestLogin(ctx, "stored")
	if res.Status != TestFailed || !strings.Contains(res.Reason, "submitted from") {
		t.Fatalf("wrong password: %+v", res)
	}
	b, _ = json.Marshal(res)
	r.shared = append(r.shared, string(b))
	if strings.Contains(string(b), "wrong-"+browsertest.Password) {
		t.Fatal("a failed test carries the value")
	}

	// A wrong one-time code: failed — the site still asks for one.
	s.SetValue("stored", "password", browsertest.Password)
	s.SetValue("stored", "otp", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXQ")
	res = r.m.TestLogin(ctx, "stored")
	if res.Status != TestFailed || !strings.Contains(res.Reason, "submitted from") {
		t.Fatalf("wrong code: %+v", res)
	}

	// A sign-in page with nothing to fill: failed, with why (the sign-in
	// itself errs — never read as signed in).
	s.SetValue("stored", "otp", "otpauth://totp/Test?secret="+testSeed)
	s.Update("stored", func(rec *LoginRecord) error { rec.LoginURL = r.bound + "/account"; return nil })
	res = r.m.TestLogin(ctx, "stored")
	if res.Status != TestFailed || !strings.Contains(res.Reason, "no sign-in field") {
		t.Fatalf("nothing to fill: %+v", res)
	}
	s.Update("stored", func(rec *LoginRecord) error { rec.LoginURL = r.bound + "/login"; return nil })

	// An approval nobody gives: needs-you.
	s.SetValue("stored", "password", browsertest.Password)
	s.ClearField("stored", "otp")
	r.site.TOTPSeed, r.site.NeedApproval = "", true
	s.Update("stored", func(rec *LoginRecord) error { rec.Approval = true; return nil })
	r.m.ApprovalWait = 3 * time.Second
	res = r.m.TestLogin(ctx, "stored")
	if res.Status != TestNeedsYou {
		t.Fatalf("approval: %+v", res)
	}
	// A credential that doesn't exist.
	if res := r.m.TestLogin(ctx, "nope"); res.Status != TestFailed {
		t.Fatalf("nope: %+v", res)
	}
	r.audit.mu.Lock()
	n := 0
	for _, e := range r.audit.es {
		if e["action"] == "test-login" {
			n++
		}
		if _, shot := e["png"]; shot {
			t.Fatal("a screenshot in the audit")
		}
	}
	r.audit.mu.Unlock()
	if n != 6 {
		t.Fatalf("test-login audited %d times", n)
	}
	r.noLeak()
}
