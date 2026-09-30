package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIsolationNetworkDecodesScalarAndMapping(t *testing.T) {
	cases := []struct {
		in   string
		want IsolationNetwork
		err  string
	}{
		{"network: open", IsolationNetwork{Mode: NetOpen}, ""},
		{"network: audit", IsolationNetwork{Mode: NetAudit}, ""},
		{"network: deny", IsolationNetwork{Mode: NetDeny}, ""},
		{"network: {egress: [proxy.golang.org]}", IsolationNetwork{Egress: []string{"proxy.golang.org"}}, ""},
		{"network: {deny: true}", IsolationNetwork{Deny: true}, ""},
		{"network: sometimes", IsolationNetwork{}, "open | audit | deny"},
		{"network: {egres: [x]}", IsolationNetwork{}, "egres"},
	}
	for _, tc := range cases {
		var v struct {
			Network *IsolationNetwork `yaml:"network"`
		}
		err := yaml.Unmarshal([]byte(tc.in), &v)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err %v, want containing %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		got := *v.Network
		if got.Mode != tc.want.Mode || got.Deny != tc.want.Deny || strings.Join(got.Egress, ",") != strings.Join(tc.want.Egress, ",") {
			t.Errorf("%s: got %+v want %+v", tc.in, got, tc.want)
		}
	}
}

func TestHostCommandForms(t *testing.T) {
	src := `
host:
  git: {}
  gh:
    deny: ["pr merge *", "repo delete *"]
  aws:
    allow: ["s3 ls *"]
    persist: ["~/.aws/sso/cache"]
  kubectl:
    env: {KUBECONFIG: ~/.kube/staging}
  docker: false
  mytool: true
`
	var iso IsolationConfig
	if err := yaml.Unmarshal([]byte(src), &iso); err != nil {
		t.Fatal(err)
	}
	if h := iso.Host["docker"]; h == nil || !h.Disabled {
		t.Fatalf("docker: false must decode as disabled: %+v", h)
	}
	if h := iso.Host["mytool"]; h == nil || h.Disabled {
		t.Fatalf("mytool: true must decode as enabled: %+v", h)
	}
	if h := iso.Host["git"]; h == nil || h.Disabled || len(h.Allow)+len(h.Deny) != 0 {
		t.Fatalf("git: {}: %+v", h)
	}
	if got := iso.Host["gh"].Deny; len(got) != 2 || got[0] != "pr merge *" {
		t.Fatalf("gh deny: %v", got)
	}
	if iso.Host["kubectl"].Env["KUBECONFIG"] != "~/.kube/staging" {
		t.Fatalf("kubectl env: %+v", iso.Host["kubectl"])
	}
	if err := validateIsolationFor("runtime r", &iso, false, true); err != nil {
		t.Fatalf("runtime-level host block must validate: %v", err)
	}
	// The same block on a step: env/persist are operator-level only.
	if err := validateAgentJail("step s", &iso, true); err == nil || !strings.Contains(err.Error(), "only narrow") {
		t.Fatalf("step-level env/persist must be refused: %v", err)
	}
	// Host rules are meaningless outside an agent launch.
	if err := validateIsolationFor("engine e", &iso, false, false); err == nil || !strings.Contains(err.Error(), "silent no-op") {
		t.Fatalf("host: on a non-agent block must be refused: %v", err)
	}
}

func TestHostCommandValidation(t *testing.T) {
	cases := []struct {
		name string
		iso  IsolationConfig
		err  string
	}{
		{"path as name", IsolationConfig{Host: map[string]*HostCommand{"/usr/bin/gh": {}}}, "not a binary name"},
		{"empty rule", IsolationConfig{Host: map[string]*HostCommand{"gh": {Deny: []string{" "}}}}, "empty rule"},
		{"persist outside home", IsolationConfig{Host: map[string]*HostCommand{"aws": {Persist: []string{"/etc/x"}}}}, "under ~/"},
		{"persist dotdot", IsolationConfig{Host: map[string]*HostCommand{"aws": {Persist: []string{"~/../x"}}}}, ".."},
		{"scalar net with egress", IsolationConfig{Network: &IsolationNetwork{Mode: NetAudit, Egress: []string{"x"}}}, "scalar mode"},
		{"audit under user", IsolationConfig{Mode: "user", User: "u", Network: &IsolationNetwork{Mode: NetAudit}}, "needs mode namespace"},
		{"neg intent", IsolationConfig{Intent: &IntentRules{MaxDeleteLines: -1}}, "max_delete_lines"},
	}
	for _, tc := range cases {
		err := validateIsolationFor("here", &tc.iso, false, true)
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s: got %v, want containing %q", tc.name, err, tc.err)
		}
	}
}

// isolation.writes is gone (#154): write authorization lives with the
// surface that performs it — the gh/git profiles under isolation.host for
// the binaries, the verb grant and the connector's scopes for conductor's
// own verbs. The key is an unknown field now.
func TestIsolationWritesIsNotAKey(t *testing.T) {
	var v IsolationConfig
	if err := strictUnmarshal([]byte("writes: read_only\n"), &v); err == nil {
		t.Fatal("isolation.writes must not decode")
	}
}

func TestAgentJailEligible(t *testing.T) {
	cases := []struct {
		cc   ControllerConfig
		want bool
	}{
		{ControllerConfig{Type: "cli", Tool: "claude-code"}, true},
		{ControllerConfig{Type: "", Agent: "gemini", Transport: "acp"}, true},
		{ControllerConfig{Type: "cli", Host: "box"}, false},
		{ControllerConfig{Type: "paseo"}, false},
		{ControllerConfig{Type: "agent-deck"}, false},
		{ControllerConfig{Type: "opencode"}, false},
		{ControllerConfig{Type: "", Agent: "x", ScrubEnv: true}, false},
	}
	for _, tc := range cases {
		if got := AgentJailEligible(tc.cc); got != tc.want {
			t.Errorf("%+v: got %v want %v", tc.cc, got, tc.want)
		}
	}
}

func TestPolicyOnlyStepIsolationOnPaseo(t *testing.T) {
	c := &Config{Controllers: map[string]ControllerConfig{"p": {Type: "paseo", Default: true}, "cli": {Type: "cli", Tool: "claude-code"}}}
	host := Step{Type: "agent", Runtime: "p", Isolation: &IsolationConfig{Host: map[string]*HostCommand{"gh": {Deny: []string{"pr merge *"}}}}}
	if err := c.validateStepIsolation("s", host); err == nil {
		t.Fatal("host: rules on a paseo step have no jail to apply to")
	}
	host.Runtime = "cli"
	if err := c.validateStepIsolation("s", host); err != nil {
		t.Fatalf("host: on a cli step: %v", err)
	}
	if !(&IsolationConfig{Intent: &IntentRules{}}).PolicyOnly() || (&IsolationConfig{Intent: &IntentRules{}, FS: []string{"/x"}}).PolicyOnly() {
		t.Fatal("PolicyOnly")
	}
}

func TestIsolationFSAcceptsHomeRelative(t *testing.T) {
	if err := validateIsolation("r", &IsolationConfig{FS: []string{"~/go", "~/.cache/go-build"}}, false); err != nil {
		t.Fatalf("fs: [~/go] is the issue's own example: %v", err)
	}
	if err := validateIsolation("r", &IsolationConfig{FS: []string{"go"}}, false); err == nil {
		t.Fatal("a relative fs: path is still refused")
	}
}
