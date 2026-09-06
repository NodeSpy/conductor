package skill

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// rig builds a broker over a fixed secret table, a movable clock, and a
// captured audit trail.
type rig struct {
	b     *Broker
	mu    sync.Mutex
	now   time.Time
	trail []map[string]any
}

func newRig(t *testing.T, vals map[string]string) *rig {
	t.Helper()
	r := &rig{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	r.b = NewBroker(
		func(name string) (string, bool) { v, ok := vals[name]; return v, ok },
		func(e map[string]any) {
			r.mu.Lock()
			r.trail = append(r.trail, e)
			r.mu.Unlock()
		},
	)
	r.b.now = func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.now
	}
	return r
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

// audits returns the recorded actions in order.
func (r *rig) audits() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.trail {
		a, _ := e["action"].(string)
		out = append(out, a)
	}
	return out
}

// auditContains fails unless some entry has the given action and the given
// key/value.
func (r *rig) auditContains(t *testing.T, action, key, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.trail {
		if e["action"] == action && e[key] == want {
			return
		}
	}
	t.Fatalf("no audit entry with action=%q %s=%q in %v", action, key, want, r.trail)
}

// noValueInAudit fails if the secret value ever reached the audit trail.
func (r *rig) noValueInAudit(t *testing.T, value string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.trail {
		for k, v := range e {
			if s, ok := v.(string); ok && strings.Contains(s, value) {
				t.Fatalf("secret value leaked into audit entry %v (key %s)", e, k)
			}
		}
	}
}

func brokerPolicy(names ...string) config.SkillPolicy {
	return config.SkillPolicy{SecretsVia: "broker", AllowSecrets: names}
}

func register(t *testing.T, b *Broker, id Identity) string {
	t.Helper()
	tok, err := b.RegisterSession(id)
	if err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	return tok
}

// Deny by default: no skill policy (zero value), secrets_via none, and
// secrets_via env all refuse broker issuance — and each refusal is audited.
func TestIssueDeniedByDefault(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	for _, pol := range []config.SkillPolicy{
		{},
		{SecretsVia: "none", AllowSecrets: []string{"deploy_key"}},
		{SecretsVia: "env", AllowSecrets: []string{"deploy_key"}},
	} {
		tok := register(t, r.b, Identity{Agent: "reviewer", Policy: pol})
		if _, _, err := r.b.Issue(tok, "deploy_key"); err == nil {
			t.Fatalf("policy %+v: issue must be denied", pol)
		} else if !strings.Contains(err.Error(), "secrets_via") {
			t.Fatalf("policy %+v: want a secrets_via denial, got %v", pol, err)
		}
	}
	r.auditContains(t, "deny", "agent", "reviewer")
}

// Allow-list gating: only the exact names in skill.allow_secrets are issued.
func TestIssueAllowListGating(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue", "other": "otherval"})
	tok := register(t, r.b, Identity{Agent: "deployer", Repo: "o/r", Policy: brokerPolicy("deploy_key")})

	if _, _, err := r.b.Issue(tok, "other"); err == nil || !strings.Contains(err.Error(), "allow_secrets") {
		t.Fatalf("issue outside allow_secrets must be denied, got %v", err)
	}
	r.auditContains(t, "deny", "secret", "other")

	id, exp, err := r.b.Issue(tok, "deploy_key")
	if err != nil {
		t.Fatalf("allowed issue: %v", err)
	}
	if !strings.HasPrefix(id, "grant-") {
		t.Fatalf("grant id %q", id)
	}
	if want := r.b.now().Add(GrantTTL); !exp.Equal(want) {
		t.Fatalf("expiry %v, want %v", exp, want)
	}
	r.auditContains(t, "issue", "secret", "deploy_key")
	r.auditContains(t, "issue", "agent", "deployer")
	r.noValueInAudit(t, "s3cretvalue")
}

// An allowed name that isn't configured (or didn't resolve) is refused
// without leaking anything about other secrets.
func TestIssueUnresolvedSecret(t *testing.T) {
	r := newRig(t, map[string]string{})
	tok := register(t, r.b, Identity{Agent: "a", Policy: brokerPolicy("ghost")})
	if _, _, err := r.b.Issue(tok, "ghost"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want not-configured denial, got %v", err)
	}
}

// Single-use: the first redeem returns the value; the second is refused and
// audited. Audit carries issue then use.
func TestRedeemSingleUse(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	tok := register(t, r.b, Identity{Agent: "deployer", Policy: brokerPolicy("deploy_key")})
	id, _, err := r.b.Issue(tok, "deploy_key")
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.b.Redeem(tok, id)
	if err != nil || v != "s3cretvalue" {
		t.Fatalf("redeem: %q, %v", v, err)
	}
	if _, err := r.b.Redeem(tok, id); err == nil || !strings.Contains(err.Error(), "single-use") {
		t.Fatalf("second redeem must be refused as single-use, got %v", err)
	}
	got := r.audits()
	if len(got) != 3 || got[0] != "issue" || got[1] != "use" || got[2] != "deny" {
		t.Fatalf("audit actions %v, want [issue use deny]", got)
	}
	r.noValueInAudit(t, "s3cretvalue")
}

// TTL expiry: a grant redeemed after GrantTTL is refused, audited as expire.
func TestGrantTTLExpiry(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	tok := register(t, r.b, Identity{Agent: "deployer", Policy: brokerPolicy("deploy_key")})
	id, _, err := r.b.Issue(tok, "deploy_key")
	if err != nil {
		t.Fatal(err)
	}
	r.advance(GrantTTL + time.Second)
	if _, err := r.b.Redeem(tok, id); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired redeem must be refused, got %v", err)
	}
	r.auditContains(t, "expire", "secret", "deploy_key")
}

// Sweep audits the expiry of a grant that was issued but never redeemed.
func TestSweepAuditsUnredeemedExpiry(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	tok := register(t, r.b, Identity{Agent: "deployer", Policy: brokerPolicy("deploy_key")})
	if _, _, err := r.b.Issue(tok, "deploy_key"); err != nil {
		t.Fatal(err)
	}
	r.advance(GrantTTL + time.Second)
	r.b.Sweep()
	r.auditContains(t, "expire", "reason", "grant expired unredeemed")
	// The grant is gone: a late redeem reads as unknown, not expired.
	r.mu.Lock()
	var grantID string
	for _, e := range r.trail {
		if e["action"] == "issue" {
			grantID, _ = e["grant"].(string)
		}
	}
	r.mu.Unlock()
	if _, err := r.b.Redeem(tok, grantID); err == nil || !strings.Contains(err.Error(), "unknown grant") {
		t.Fatalf("swept grant must read as unknown, got %v", err)
	}
}

// Grants are scoped to the issuing session: another live session's token
// cannot redeem them, and learns nothing (reads as unknown grant).
func TestRedeemScopedToIssuingSession(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	tokA := register(t, r.b, Identity{Agent: "deployer", Policy: brokerPolicy("deploy_key")})
	tokB := register(t, r.b, Identity{Agent: "reviewer", Policy: brokerPolicy("deploy_key")})
	id, _, err := r.b.Issue(tokA, "deploy_key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.b.Redeem(tokB, id); err == nil || !strings.Contains(err.Error(), "unknown grant") {
		t.Fatalf("cross-session redeem must read as unknown grant, got %v", err)
	}
	// The rightful session still can.
	if v, err := r.b.Redeem(tokA, id); err != nil || v != "s3cretvalue" {
		t.Fatalf("rightful redeem: %q, %v", v, err)
	}
}

// Identity is bound server-side: a fabricated token is refused outright — no
// client-asserted field can stand in for a minted session.
func TestUnknownTokenRefused(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	if _, _, err := r.b.Issue("forged-token", "deploy_key"); err == nil || !strings.Contains(err.Error(), "session token") {
		t.Fatalf("forged token must be refused, got %v", err)
	}
	if _, err := r.b.Redeem("forged-token", "grant-x"); err == nil {
		t.Fatal("forged redeem must be refused")
	}
	r.auditContains(t, "deny", "reason", "unknown or expired session token")
}

// A session token past SessionTTL no longer issues.
func TestSessionExpiry(t *testing.T) {
	r := newRig(t, map[string]string{"deploy_key": "s3cretvalue"})
	tok := register(t, r.b, Identity{Agent: "deployer", Policy: brokerPolicy("deploy_key")})
	r.advance(SessionTTL + time.Second)
	if _, _, err := r.b.Issue(tok, "deploy_key"); err == nil || !strings.Contains(err.Error(), "session token") {
		t.Fatalf("expired session must be refused, got %v", err)
	}
	// Sweep drops it entirely.
	r.b.Sweep()
	r.b.mu.Lock()
	n := len(r.b.sessions)
	r.b.mu.Unlock()
	if n != 0 {
		t.Fatalf("swept sessions remaining: %d", n)
	}
}

// Tokens are distinct and unguessable-length; two sessions never collide.
func TestTokenMinting(t *testing.T) {
	r := newRig(t, nil)
	a := register(t, r.b, Identity{Agent: "a"})
	b := register(t, r.b, Identity{Agent: "b"})
	if a == b {
		t.Fatal("token collision")
	}
	if len(a) != 64 {
		t.Fatalf("token length %d, want 64 hex chars (32 bytes)", len(a))
	}
}
