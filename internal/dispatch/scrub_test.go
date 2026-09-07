package dispatch

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// #122 R3: tracked secret values are scrubbed from the template scope an
// external runtime renders against — step outputs, trigger context — while
// the intentional credential channels (secrets/vaults scopes, dispatch
// tokens) pass through and ordinary data flows normally.
func TestTemplateDataScrubsTrackedSecrets(t *testing.T) {
	res := secrets.New()
	res.Track("s3kr1t-value")
	SetScrubber(res)
	t.Cleanup(func() { SetScrubber(nil) })

	req := Request{
		Trigger: core.Trigger{
			Kind:    "ping",
			Target:  core.Target{Repo: "o/r", Number: 7},
			Context: map[string]any{"note": "ctx s3kr1t-value here"},
		},
		Tokens: Tokens{User: "utok", App: "atok"},
		Data: map[string]any{
			"build":   map[string]any{"log": "curl -H 'Authorization: s3kr1t-value'", "ok": true},
			"plain":   "keep me",
			"secrets": map[string]any{"tok": "s3kr1t-value"},
			"vaults":  map[string]any{"house": map[string]any{"k": "s3kr1t-value"}},
		},
	}
	data := templateData(req)

	log := data["build"].(map[string]any)["log"].(string)
	if strings.Contains(log, "s3kr1t-value") || !strings.Contains(log, secrets.Placeholder) {
		t.Fatalf("step output must be scrubbed: %q", log)
	}
	if note := data["note"].(string); strings.Contains(note, "s3kr1t-value") {
		t.Fatalf("trigger context must be scrubbed: %q", note)
	}
	if data["plain"] != "keep me" || data["build"].(map[string]any)["ok"] != true {
		t.Fatalf("ordinary data must flow: %+v", data)
	}
	// The explicit channels keep the real values.
	if data["secrets"].(map[string]any)["tok"] != "s3kr1t-value" {
		t.Fatalf("the secrets scope is the intentional channel: %+v", data["secrets"])
	}
	if data["vaults"].(map[string]any)["house"].(map[string]any)["k"] != "s3kr1t-value" {
		t.Fatalf("the vaults scope is the intentional channel: %+v", data["vaults"])
	}
	if data["gh_token"] != "utok" || data["app_token"] != "atok" {
		t.Fatalf("dispatch tokens must pass: %+v", data)
	}

	// And the rendered prompt an agent receives shows the placeholder.
	req.Action.Prompt = "fix using {{.build.log}}"
	prompt, err := RenderPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "s3kr1t-value") {
		t.Fatalf("rendered prompt leaked the value: %q", prompt)
	}

	// No scrubber configured (legacy boot) → passthrough, no panic.
	SetScrubber(nil)
	if d := templateData(req); !strings.Contains(d["build"].(map[string]any)["log"].(string), "s3kr1t-value") {
		t.Fatal("without a scrubber the data must pass through")
	}
}
