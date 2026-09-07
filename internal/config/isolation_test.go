package config

import (
	"strings"
	"testing"
)

// isoBase returns a minimal valid connectors-model config with one acp
// runtime, one host, and one agent profile, ready for isolation blocks.
func isoBase(t *testing.T) *Config {
	t.Helper()
	c := &Config{
		ConnectorsMap: map[string]ConnectorRef{"gh": {Type: "github"}},
		Runtimes: map[string]RuntimeConfig{
			"gemini": {Agent: "gemini"},
			"pd":     {Type: "paseo"},
		},
		Hosts:  map[string]HostConfig{"sbx": {Host: "sandbox.internal"}},
		Agents: map[string]AgentProfile{},
		Triggers: []TriggerSpec{{On: "gh.release", Steps: []Step{
			{Uses: "gh.comment"},
		}}},
	}
	return c
}

func TestIsolationValidation(t *testing.T) {
	old := isolationGOOS
	isolationGOOS = "linux"
	defer func() { isolationGOOS = old }()

	cases := []struct {
		name    string
		iso     *IsolationConfig
		remote  bool
		wantErr string
	}{
		{"nil ok", nil, false, ""},
		{"missing mode", &IsolationConfig{}, false, "mode: user|namespace|container"},
		{"unknown mode", &IsolationConfig{Mode: "jail"}, false, "unknown isolation mode"},
		{"user without user", &IsolationConfig{Mode: "user"}, false, "needs `user:`"},
		{"user ok", &IsolationConfig{Mode: "user", User: "sbx"}, false, ""},
		{"namespace ok on linux", &IsolationConfig{Mode: "namespace"}, false, ""},
		{"namespace ok remote", &IsolationConfig{Mode: "namespace"}, true, ""},
		{"container needs image", &IsolationConfig{Mode: "container"}, false, "container.image"},
		{"container ok", &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "i"}}, false, ""},
		{"container bad engine", &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "i", Engine: "lxc"}}, false, "docker|podman"},
		{"container remote rejected", &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "i"}}, true, "not supported with a remote host"},
		// deny+egress together is the ENFORCED allowlist (#36 iso-review C1)
		// under the structural modes; user mode still can't structurally deny.
		{"deny+egress enforced namespace", &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}, false, ""},
		{"deny+egress enforced container", &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "i"}, Network: &IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}, false, ""},
		{"deny+egress user rejected", &IsolationConfig{Mode: "user", User: "s", Network: &IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}, false, "structural mode"},
		{"deny+egress remote rejected", &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}, true, "local launch"},
		{"deny needs structural mode", &IsolationConfig{Mode: "user", User: "s", Network: &IsolationNetwork{Deny: true}}, false, "structural mode"},
		{"deny ok namespace", &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true}}, false, ""},
		{"deny ok remote namespace", &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true}}, true, ""},
		{"egress remote rejected", &IsolationConfig{Mode: "user", User: "s", Network: &IsolationNetwork{Egress: []string{"a:443"}}}, true, "local launch"},
		{"empty egress pattern", &IsolationConfig{Mode: "user", User: "s", Network: &IsolationNetwork{Egress: []string{""}}}, false, "empty pattern"},
		{"negative pids", &IsolationConfig{Mode: "user", User: "s", Limits: &IsolationLimits{Pids: -1}}, false, "pids"},
	}
	for _, tc := range cases {
		err := validateIsolation("here", tc.iso, tc.remote)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestIsolationNamespaceRejectedOffLinux(t *testing.T) {
	old := isolationGOOS
	isolationGOOS = "darwin"
	defer func() { isolationGOOS = old }()
	err := validateIsolation("here", &IsolationConfig{Mode: "namespace"}, false)
	if err == nil || !strings.Contains(err.Error(), "Linux-only") {
		t.Fatalf("namespace off linux: %v", err)
	}
	// Remote skips the local GOOS check (the wrapper runs on the remote box).
	if err := validateIsolation("here", &IsolationConfig{Mode: "namespace"}, true); err != nil {
		t.Fatalf("remote namespace off linux: %v", err)
	}
}

func TestProfileIsolationNeedsConductorLaunchedRuntime(t *testing.T) {
	iso := &IsolationConfig{Mode: "user", User: "sbx"}

	// No runtime at all → built-in paseo → rejected.
	c := isoBase(t)
	c.Runtimes = nil
	c.Agents["fixer"] = AgentProfile{Isolation: iso}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "built-in paseo") {
		t.Fatalf("builtin paseo: %v", err)
	}

	// A paseo runtime → rejected.
	c = isoBase(t)
	c.Agents["fixer"] = AgentProfile{Runtime: "pd", Isolation: iso}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "paseo runtime") {
		t.Fatalf("paseo runtime: %v", err)
	}

	// An acp runtime → fine.
	c = isoBase(t)
	c.Agents["fixer"] = AgentProfile{Runtime: "gemini", Isolation: iso}
	if err := c.Validate(); err != nil {
		t.Fatalf("acp runtime: %v", err)
	}
}

func TestRuntimeIsolationValidation(t *testing.T) {
	iso := &IsolationConfig{Mode: "user", User: "sbx"}

	c := isoBase(t)
	rt := c.Runtimes["pd"]
	rt.Isolation = iso
	c.Runtimes["pd"] = rt
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "paseo runtime") {
		t.Fatalf("paseo runtime isolation: %v", err)
	}

	c = isoBase(t)
	rt = c.Runtimes["gemini"]
	rt.Isolation = iso
	c.Runtimes["gemini"] = rt
	if err := c.Validate(); err != nil {
		t.Fatalf("acp runtime isolation: %v", err)
	}

	// Opencode + structural deny severs the control channel → rejected.
	c = isoBase(t)
	c.Runtimes["oc"] = RuntimeConfig{Type: "opencode", Isolation: &IsolationConfig{
		Mode: "namespace", Network: &IsolationNetwork{Deny: true}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "control channel") {
		t.Fatalf("opencode deny: %v", err)
	}
}

func TestHostIsolationValidation(t *testing.T) {
	c := isoBase(t)
	c.Hosts["sbx"] = HostConfig{Host: "sandbox.internal",
		Isolation: &IsolationConfig{Mode: "user", User: "agents"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("host isolation user: %v", err)
	}

	// Container mode on a host is rejected (remote).
	c.Hosts["sbx"] = HostConfig{Host: "sandbox.internal",
		Isolation: &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "i"}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "remote host") {
		t.Fatalf("host isolation container: %v", err)
	}
}

func TestIsolationCarriedToControllerConfig(t *testing.T) {
	iso := &IsolationConfig{Mode: "user", User: "sbx"}
	rt := RuntimeConfig{Agent: "gemini", Isolation: iso}
	if got := rt.Controller().Isolation; got != iso {
		t.Fatalf("Controller() must carry Isolation through, got %v", got)
	}
}

func TestBudgetValidation(t *testing.T) {
	ok := &BudgetPolicy{MaxCostUSD: 5}
	if err := validateBudget("here", ok); err != nil {
		t.Fatalf("valid budget: %v", err)
	}
	if err := validateBudget("here", &BudgetPolicy{}); err == nil ||
		!strings.Contains(err.Error(), "caps nothing") {
		t.Fatalf("empty budget: %v", err)
	}
	if err := validateBudget("here", &BudgetPolicy{MaxCostUSD: -1}); err == nil {
		t.Fatal("negative $ cap")
	}
	if err := validateBudget("here", &BudgetPolicy{MaxTokens: -1}); err == nil {
		t.Fatal("negative token cap")
	}
	// Default window.
	if w := (&BudgetPolicy{}).WindowOrDefault(); w != DefaultBudgetWindow {
		t.Fatalf("default window: %v", w)
	}
	var nilB *BudgetPolicy
	if w := nilB.WindowOrDefault(); w != DefaultBudgetWindow {
		t.Fatalf("nil window: %v", w)
	}
}

func TestBudgetInPolicyAndProfileValidated(t *testing.T) {
	c := isoBase(t)
	c.Policy = &Policy{Budget: &BudgetPolicy{}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("global budget validation: %v", err)
	}
	c = isoBase(t)
	c.Agents["fixer"] = AgentProfile{Budget: &BudgetPolicy{MaxTokens: -5}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "agent fixer: budget") {
		t.Fatalf("profile budget validation: %v", err)
	}
}

func TestPricingValidation(t *testing.T) {
	c := isoBase(t)
	c.Pricing = &PricingConfig{Models: map[string]ModelPrice{"claude-*": {Input: 3, Output: 15}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid pricing: %v", err)
	}
	c.Pricing = &PricingConfig{Models: map[string]ModelPrice{"": {Input: 3}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "empty model pattern") {
		t.Fatalf("empty pattern: %v", err)
	}
	c.Pricing = &PricingConfig{Default: &ModelPrice{Input: -1}}
	if err := c.Validate(); err == nil {
		t.Fatal("negative price")
	}
}

func TestBudgetMergePolicy(t *testing.T) {
	g := &Policy{Budget: &BudgetPolicy{MaxCostUSD: 10}}
	tr := &Policy{Budget: &BudgetPolicy{MaxCostUSD: 1}}
	if got := MergePolicy(g, nil, tr).Budget.MaxCostUSD; got != 1 {
		t.Fatalf("trigger budget must win: %v", got)
	}
	if got := MergePolicy(g, nil, nil).Budget.MaxCostUSD; got != 10 {
		t.Fatalf("global fallback: %v", got)
	}
}

// Regression (#36 iso-review H7): privileged is the namespace-mode opt-out
// of default filesystem masking; anywhere else it would be a silent no-op,
// so it is rejected.
func TestIsolationPrivilegedKnob(t *testing.T) {
	ok := &IsolationConfig{Mode: "namespace", Privileged: true}
	if err := validateIsolation("here", ok, false); err != nil {
		t.Fatalf("privileged namespace: %v", err)
	}
	bad := &IsolationConfig{Mode: "user", User: "s", Privileged: true}
	if err := validateIsolation("here", bad, false); err == nil || !strings.Contains(err.Error(), "privileged") {
		t.Fatalf("privileged on user mode must be rejected: %v", err)
	}
	badC := &IsolationConfig{Mode: "container", Container: &ContainerIsolation{Image: "i"}, Privileged: true}
	if err := validateIsolation("here", badC, false); err == nil {
		t.Fatal("privileged on container mode must be rejected")
	}
}

// Regression (#36 iso-review C3): the one-shot skill claim rides the tool
// server's env — under shared-uid mode:user a sibling reads it from
// /proc/<pid>/environ and races the claim, re-opening the broker-identity
// hijack. skill: + effective mode:user isolation is refused at load, from
// both the profile's own block and an inherited runtime block.
func TestSkillRefusedUnderUserModeIsolation(t *testing.T) {
	base := func() *Config {
		return &Config{
			Runtimes: map[string]RuntimeConfig{
				"cc": {Type: "cli", Agent: "claude-code"},
			},
			Agents: map[string]AgentProfile{},
		}
	}

	// Profile-level mode:user + skill → refused.
	c := base()
	c.Agents["fixer"] = AgentProfile{Runtime: "cc", Provider: "claude", Model: "m",
		Skill:     &SkillPolicy{},
		Isolation: &IsolationConfig{Mode: "user", User: "sbx"}}
	if err := c.validateSkillIsolation("fixer", c.Agents["fixer"]); err == nil ||
		!strings.Contains(err.Error(), "steals the claim") {
		t.Fatalf("profile-level user isolation + skill must be refused: %v", err)
	}

	// Runtime-level mode:user inherited by a skill profile → refused too.
	c = base()
	rt := c.Runtimes["cc"]
	rt.Isolation = &IsolationConfig{Mode: "user", User: "sbx"}
	c.Runtimes["cc"] = rt
	c.Agents["fixer"] = AgentProfile{Runtime: "cc", Provider: "claude", Model: "m", Skill: &SkillPolicy{}}
	if err := c.validateSkillIsolation("fixer", c.Agents["fixer"]); err == nil {
		t.Fatal("runtime-level user isolation + skill must be refused")
	}

	// namespace isolation (separate /proc views) keeps skill available.
	c = base()
	c.Agents["fixer"] = AgentProfile{Runtime: "cc", Provider: "claude", Model: "m",
		Skill:     &SkillPolicy{},
		Isolation: &IsolationConfig{Mode: "namespace"}}
	if err := c.validateSkillIsolation("fixer", c.Agents["fixer"]); err != nil {
		t.Fatalf("namespace + skill must be fine: %v", err)
	}
	// And a profile's own non-user isolation overrides a user-mode runtime.
	c = base()
	rt = c.Runtimes["cc"]
	rt.Isolation = &IsolationConfig{Mode: "user", User: "sbx"}
	c.Runtimes["cc"] = rt
	c.Agents["fixer"] = AgentProfile{Runtime: "cc", Provider: "claude", Model: "m",
		Skill:     &SkillPolicy{},
		Isolation: &IsolationConfig{Mode: "namespace"}}
	if err := c.validateSkillIsolation("fixer", c.Agents["fixer"]); err != nil {
		t.Fatalf("profile namespace overrides runtime user: %v", err)
	}
}
