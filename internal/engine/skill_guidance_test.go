package engine

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The skill guidance blurb (#36 §12 increment 4): appended through the same
// path as agent_guidance, opt-in by the profile's skill: block, and always
// redactor-filtered.
func TestSkillGuidance(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)

	// Absent by default: a profile without skill: gets only the house text.
	plain := e.agentGuidance(config.AgentProfile{})
	if strings.Contains(plain, "Conductor tools") {
		t.Fatalf("skill guidance leaked into a plain profile: %q", plain)
	}

	// Present when opted in, naming the verb patterns and broker secrets.
	sk := config.AgentProfile{Skill: &config.SkillPolicy{
		Verbs: []string{"gh.comment", "rest.*"}, SecretsVia: "broker", AllowSecrets: []string{"deploy_key"},
	}}
	got := e.agentGuidance(sk)
	for _, want := range []string{"Conductor tools", "gh.comment, rest.*", "secret_issue", "deploy_key", "single-use", "«secret:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("guidance missing %q: %q", want, got)
		}
	}
	// secrets_via env/none never advertises the broker.
	envProf := config.AgentProfile{Skill: &config.SkillPolicy{SecretsVia: "env", AllowSecrets: []string{"deploy_key"}}}
	if g := e.agentGuidance(envProf); strings.Contains(g, "secret_issue") {
		t.Fatalf("broker guidance without secrets_via broker: %q", g)
	}

	// Redactor-filtered: a tracked value can never ride the guidance path.
	res := secrets.New()
	res.Track("s3kr1t-value")
	e.secrets = res
	leaky := "never say s3kr1t-value"
	withLeak := config.AgentProfile{Guidance: &leaky, Skill: sk.Skill}
	g := e.agentGuidance(withLeak)
	if strings.Contains(g, "s3kr1t-value") {
		t.Fatalf("guidance leaked a tracked value: %q", g)
	}
	if !strings.Contains(g, secrets.Placeholder) {
		t.Fatalf("guidance must be redacted: %q", g)
	}
}
