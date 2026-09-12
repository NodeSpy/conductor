package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestSessionSpecParseAndDefaults(t *testing.T) {
	var p Step
	y := `
session:
  key: "{{.repo}}#{{.pr}}"
  idle_ttl: 12h
  max_lifetime: 7d
  end_on: [ gh.pr_closed, gh.merged ]
`
	if err := yaml.Unmarshal([]byte(y), &p); err != nil {
		t.Fatal(err)
	}
	s := p.Session
	if s == nil || s.Key != "{{.repo}}#{{.pr}}" {
		t.Fatalf("session: %+v", s)
	}
	if s.IdleTTLOrDefault() != 12*time.Hour || s.MaxLifetimeOrDefault() != 7*24*time.Hour {
		t.Fatalf("ttls: %v %v", s.IdleTTLOrDefault(), s.MaxLifetimeOrDefault())
	}
	// Defaults when unset: bounded, never forever.
	bare := &SessionSpec{Key: "k"}
	if bare.IdleTTLOrDefault() != DefaultSessionIdleTTL || bare.MaxLifetimeOrDefault() != DefaultSessionMaxLifetime {
		t.Fatalf("defaults: %v %v", bare.IdleTTLOrDefault(), bare.MaxLifetimeOrDefault())
	}
	// No session: block → nil.
	var p2 Step
	if err := yaml.Unmarshal([]byte("model: x"), &p2); err != nil || p2.Session != nil {
		t.Fatalf("absent session: %+v %v", p2.Session, err)
	}
}

func TestSessionEndsOn(t *testing.T) {
	s := &SessionSpec{EndOn: []string{"gh.pr_closed", "merged"}}
	cases := []struct {
		instance, source, kind string
		want                   bool
	}{
		{"gh", "github", "pr_closed", true}, // instance.kind
		{"hub", "gh", "pr_closed", true},    // source.kind
		{"gh", "github", "merged", true},    // bare kind
		{"gh", "github", "new_comment", false},
		{"other", "github", "pr_closed", false},
	}
	for _, c := range cases {
		if got := s.EndsOn(c.instance, c.source, c.kind); got != c.want {
			t.Errorf("EndsOn(%s,%s,%s) = %v, want %v", c.instance, c.source, c.kind, got, c.want)
		}
	}
}

// A session: block is legal at two scopes now (design §3): on a RUNTIME (the
// overall pool) and on a STEP (its own). Both go through the same check.
func TestValidateSessions(t *testing.T) {
	step := func(s *SessionSpec) *Config {
		return &Config{Workflows: map[string]WorkflowDef{"w": {Steps: []Step{{ID: "a", Session: s}}}}}
	}
	runtime := func(s *SessionSpec) *Config {
		return &Config{Runtimes: RuntimeSet{"paseo": {Use: "paseo", Session: s}}}
	}
	for name, base := range map[string]func(*SessionSpec) *Config{"step": step, "runtime": runtime} {
		check := func(c *Config) error {
			if err := c.validateSessions(); err != nil {
				return err
			}
			return c.validateSteps()
		}
		if err := check(base(nil)); err != nil {
			t.Fatalf("%s nil session: %v", name, err)
		}
		if err := check(base(&SessionSpec{Key: "{{.repo}}#{{.pr}}"})); err != nil {
			t.Fatalf("%s valid: %v", name, err)
		}
		if err := check(base(&SessionSpec{})); err == nil || !strings.Contains(err.Error(), "session.key is required") {
			t.Fatalf("%s missing key: %v", name, err)
		}
		if err := check(base(&SessionSpec{Key: "{{.repo"})); err == nil {
			t.Fatalf("%s bad template must error", name)
		}
		if err := check(base(&SessionSpec{Key: "k", EndOn: []string{""}})); err == nil {
			t.Fatalf("%s empty end_on entry must error", name)
		}
	}
}
