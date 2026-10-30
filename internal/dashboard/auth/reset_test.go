package auth

import (
	"strings"
	"testing"
)

// A reset says everything it removed: passkeys, sessions, the password, the
// authenticator and a live enrolment link.
func TestResetReportsEverythingItRemoves(t *testing.T) {
	h := newHarness(t)
	st := h.m.deps.Store
	_ = st.UpdateOperatorSecret("ops", func(o *OperatorSecret) { o.PasswordHash = "x"; o.TOTPSecret = "y" })
	if _, _, err := h.m.CreateSession("ops", ListenerLocalhost, "", "a browser", "127.0.0.1:1", ""); err != nil {
		t.Fatal(err)
	}
	res := h.m.ResetOperator("ops")
	if !res.Password || !res.TOTP || res.Sessions != 1 {
		t.Fatalf("reset = %+v", res)
	}
	if got := res.String(); !strings.Contains(got, "the password") || !strings.Contains(got, "the authenticator") || !strings.Contains(got, "1 session(s)") {
		t.Fatalf("reset in words: %q", got)
	}
	if rec, _ := st.OperatorSecret("ops"); rec.PasswordHash != "" || rec.TOTPSecret != "" {
		t.Fatalf("the password and authenticator must be gone: %+v", rec)
	}
	if again := h.m.ResetOperator("ops"); again.Password || again.TOTP || again.String() != "0 passkey(s) and 0 session(s) removed" {
		t.Fatalf("a second reset removes nothing: %+v %q", again, again.String())
	}
}
