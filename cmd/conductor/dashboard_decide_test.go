package main

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/dashboard"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// The approval callers stay method-agnostic: the daemon maps whatever stepped
// up onto the bot decision — a passkey to webauthn, anything else to dashboard.
func TestDashboardDecisionMapsStepupMethod(t *testing.T) {
	pk := dashboardDecision("b", "p", "approve", "op", "", dashboard.StepupEvidence{Method: auth.MethodPasskey, CredID: "c1"})
	if pk.Method != "webauthn" || pk.WebAuthn == nil || pk.WebAuthn.Credential != "c1" || pk.Dashboard != nil {
		t.Fatalf("passkey: %+v", pk)
	}
	ssh := dashboardDecision("b", "p", "approve", "op", "", dashboard.StepupEvidence{Method: auth.MethodSSH, SSHPayload: "x", SSHSignature: "sig"})
	if ssh.Method != "dashboard" || ssh.Dashboard == nil || ssh.Dashboard.Method != "ssh" || ssh.Dashboard.SSHSignature != "sig" || ssh.WebAuthn != nil {
		t.Fatalf("ssh: %+v", ssh)
	}
	totp := dashboardDecision("b", "p", "deny", "op", "", dashboard.StepupEvidence{Method: auth.MethodTOTP})
	if totp.Method != "dashboard" || totp.Dashboard.Method != "totp" {
		t.Fatalf("totp: %+v", totp)
	}
}
