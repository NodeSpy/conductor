package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestSessionSpecParseAndDefaults(t *testing.T) {
	var p AgentProfile
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
	var p2 AgentProfile
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

func TestValidateSessions(t *testing.T) {
	base := func(s *SessionSpec) *Config {
		return &Config{Agents: map[string]AgentProfile{"a": {Session: s}}}
	}
	if err := base(nil).validateSessions(); err != nil {
		t.Fatalf("nil session: %v", err)
	}
	if err := base(&SessionSpec{Key: "{{.repo}}#{{.pr}}"}).validateSessions(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := base(&SessionSpec{}).validateSessions(); err == nil || !strings.Contains(err.Error(), "session.key is required") {
		t.Fatalf("missing key: %v", err)
	}
	if err := base(&SessionSpec{Key: "{{.repo"}).validateSessions(); err == nil {
		t.Fatal("bad template must error")
	}
	if err := base(&SessionSpec{Key: "k", EndOn: []string{""}}).validateSessions(); err == nil {
		t.Fatal("empty end_on entry must error")
	}
}
