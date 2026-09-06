package flow

import (
	"strings"
	"testing"
)

// The env-secret deprecation lint (#36 §12 increment 5): validate WARNS (it
// never fails) when a secret is templated into an AGENT step's env:.
func TestDeprecationWarningsAgentEnvSecrets(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
secrets:
  tok: env:FLOW_DEP_TEST_TOK
vaults:
  house: { type: file, dir: /tmp }
agents:
  deployer: { model: x }
triggers:
  - on: svc.ping
    steps:
      - id: legacy
        type: agent
        agent: deployer
        prompt: p
        env:
          TOKEN: "{{.secrets.tok}}"
          VAULTED: '{{ vault "house" "k" }}'
          FIELD: "{{.vaults.house.k}}"
          FINE: "plain-value"
          HANDLE: '{{secret "tok"}}'
      - id: code
        run: sh
        code: "true"
        env:
          TOKEN: "{{.secrets.tok}}"
workflows:
  deploy:
    steps:
      - id: wfagent
        type: agent
        agent: deployer
        prompt: p
        env:
          T: "{{.secrets.tok}}"
`)
	warns := DeprecationWarnings(cfg)
	joined := strings.Join(warns, "\n")
	for _, want := range []string{
		"triggers[0].steps[legacy]: env.TOKEN",
		"triggers[0].steps[legacy]: env.VAULTED",
		"triggers[0].steps[legacy]: env.FIELD",
		"workflows.deploy.steps[wfagent]: env.T",
		"DEPRECATED",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing warning %q in:\n%s", want, joined)
		}
	}
	if len(warns) != 4 {
		t.Fatalf("want exactly 4 warnings, got %d:\n%s", len(warns), joined)
	}
	// The replacement pattern and plain values are never flagged, and code
	// steps (conductor-side exec, resolved at the boundary) aren't either.
	for _, notWant := range []string{"env.FINE", "env.HANDLE", "steps[code]"} {
		if strings.Contains(joined, notWant) {
			t.Fatalf("false positive %q in:\n%s", notWant, joined)
		}
	}

	// A config with no agent-env secrets warns nothing.
	clean := loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - on: svc.ping
    steps: [ { id: a, uses: svc.post, options: { text: hi } } ]
`)
	if got := DeprecationWarnings(clean); len(got) != 0 {
		t.Fatalf("clean config must not warn: %v", got)
	}
}
