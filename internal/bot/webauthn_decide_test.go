package bot

import (
	"context"
	"testing"
)

// The dashboard's WebAuthn approval path (design §6.6): when Method is
// "webauthn" the SSHSIG check is skipped (the dashboard verified the assertion
// in process), but every other guard still runs and the decision executes the
// exact signed action. Evidence is required and recorded.
func TestDecideWebAuthnPath(t *testing.T) {
	f := newExecFixture(t, testBot())
	pid := f.propose(t, "gh.comment", map[string]any{"body": "hi"})

	// No evidence → refused even on the webauthn path.
	if _, err := f.x.Decide(context.Background(), Decision{
		Bot: "sre", Proposal: pid, Operator: "op", Decision: "approve", Method: "webauthn",
	}); err == nil {
		t.Fatal("a webauthn decision with no assertion evidence must be refused")
	}

	// With evidence and a real operator → executes, no signature needed.
	out, err := f.x.Decide(context.Background(), Decision{
		Bot: "sre", Proposal: pid, Operator: "op", Decision: "approve", Method: "webauthn",
		WebAuthn: &WebAuthnEvidence{Credential: "pk1", AuthenticatorData: "AA", ClientDataJSON: "BB", Signature: "CC"},
	})
	if err != nil || out.Status != "executed" {
		t.Fatalf("webauthn approve: status=%q err=%v", out.Status, err)
	}
	if len(f.ran) != 1 || f.ran[0] != "gh.comment" {
		t.Fatalf("ran %v", f.ran)
	}
}

// A non-operator cannot approve on the webauthn path either (membership is still
// checked).
func TestDecideWebAuthnNonOperatorRefused(t *testing.T) {
	f := newExecFixture(t, testBot())
	pid := f.propose(t, "gh.comment", map[string]any{"body": "hi"})
	if _, err := f.x.Decide(context.Background(), Decision{
		Bot: "sre", Proposal: pid, Operator: "intruder", Decision: "approve", Method: "webauthn",
		WebAuthn: &WebAuthnEvidence{Credential: "pk1"},
	}); err == nil {
		t.Fatal("a non-operator must not approve, even with webauthn evidence")
	}
}
