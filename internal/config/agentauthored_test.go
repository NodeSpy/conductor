package config

import (
	"strings"
	"testing"
)

// Regression (#36 iso-review H6): policy.agent_authored.host names the box
// agent code is FORCED onto — it must carry an isolation: block, or the
// "sandbox" is a plain remote shell wearing the name. trust: full is the
// documented, deliberate opt-out.
func TestAgentAuthoredHostMustIsolate(t *testing.T) {
	hosts := map[string]HostConfig{
		"bare":    {Host: "bare.internal"},
		"sandbox": {Host: "sbx.internal", Isolation: &IsolationConfig{Mode: "user", User: "agents"}},
	}

	// A host without isolation is refused by default.
	p := &AgentAuthoredPolicy{Host: "bare"}
	err := validateAgentAuthored("policy", p, hosts)
	if err == nil || !strings.Contains(err.Error(), "no isolation") {
		t.Fatalf("bare sandbox host must be rejected: %v", err)
	}

	// An actually-isolating host passes.
	p = &AgentAuthoredPolicy{Host: "sandbox"}
	if err := validateAgentAuthored("policy", p, hosts); err != nil {
		t.Fatalf("isolating host: %v", err)
	}

	// trust: full is the explicit opt-out — the operator who lifted the
	// allow/approve/host gates has said "I know".
	p = &AgentAuthoredPolicy{Host: "bare", Trust: "full"}
	if err := validateAgentAuthored("policy", p, hosts); err != nil {
		t.Fatalf("trust: full must opt out of the requirement: %v", err)
	}

	// Unknown host still errors first.
	p = &AgentAuthoredPolicy{Host: "nope"}
	if err := validateAgentAuthored("policy", p, hosts); err == nil ||
		!strings.Contains(err.Error(), "not a hosts: entry") {
		t.Fatalf("unknown host: %v", err)
	}
	// The scoped pass (hosts unseen) defers to the global re-check.
	p = &AgentAuthoredPolicy{Host: "bare"}
	if err := validateAgentAuthored("scoped", p, nil); err != nil {
		t.Fatalf("scoped pass defers: %v", err)
	}
}
