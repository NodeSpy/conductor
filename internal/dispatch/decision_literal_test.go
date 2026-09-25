package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The attack a decide step's prompt must survive: its state is someone
// else's text (a PR diff), already rendered once by the flow runner. A
// second templating pass would evaluate a {{.gh_token}} the author planted
// in the diff against the dispatch's template data — credentials included.
const plantedDiff = "DIFF:\n+ token: {{.gh_token}}\n+ helm: {{ .Values.image }}\n+ broken: {{ if"

func decisionRequest() Request {
	return Request{
		Trigger: core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "o/r", PR: 7, Number: 7}},
		Wait:    true,
		Action: config.Action{Type: "agent", Prompt: plantedDiff, Checkout: "none",
			OutputSchema: map[string]any{"type": "object"}},
		Step:      config.Step{Type: "agent", DecisionLaunch: &config.DecisionLaunch{Document: plantedDiff}},
		Tokens:    Tokens{User: "ghp_SECRET_USER_TOKEN", App: "ghs_SECRET_APP_TOKEN"},
		Workspace: "wks1",
	}
}

func TestDecisionPromptIsNeverTemplated(t *testing.T) {
	got, err := RenderPrompt(decisionRequest())
	if err != nil {
		t.Fatalf("a decision prompt with template syntax in its data must not fail to render: %v", err)
	}
	if strings.Contains(got, "ghp_SECRET_USER_TOKEN") || strings.Contains(got, "ghs_SECRET_APP_TOKEN") {
		t.Fatalf("a planted {{.gh_token}} was evaluated into the prompt:\n%s", got)
	}
	if !strings.Contains(got, "{{.gh_token}}") || !strings.Contains(got, "{{ .Values.image }}") {
		t.Fatalf("the prompt must carry the diff verbatim:\n%s", got)
	}
}

// The same holds on the paseo path, which renders its own prompt.
func TestDecisionPromptIsNeverTemplatedOnPaseo(t *testing.T) {
	fb := &scriptedRunAgentBackend{responses: []func(RunAgentOptions) (RunAgentResult, error){
		textResult(`{"answers":{}}`),
	}}
	d := &Dispatcher{PaseoBin: "paseo"}
	d.SetBackend(fb)
	if _, err := d.paseo(context.Background(), decisionRequest()); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	prompt := fb.calls[0].Args[1]
	if strings.Contains(prompt, "SECRET") {
		t.Fatalf("a planted {{.gh_token}} was evaluated into the paseo prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "{{.gh_token}}") {
		t.Fatalf("the paseo prompt must carry the diff verbatim:\n%s", prompt)
	}
}

// An ordinary agent prompt is still a template.
func TestAgentPromptStillRenders(t *testing.T) {
	req := decisionRequest()
	req.Step.DecisionLaunch = nil
	req.Action.Prompt = "fix {{.repo}}#{{.pr}}"
	req.Action.OutputSchema = nil
	if got, err := RenderPrompt(req); err != nil || got != "fix o/r#7" {
		t.Fatalf("got %q, %v", got, err)
	}
}
