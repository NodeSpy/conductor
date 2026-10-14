package flow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// probeHandler is a minimal fixture plugin (plugin-contract.md §1.10) whose
// events exercise every shape of the `target.assigned` declaration:
// statically true, statically false, a fact (which MAY be false at any one
// occurrence), and no target semantics at all. It proves
// untrustedTargetWarnings (A6, §3.7) is driven purely by the declaration —
// it has nothing webhook-specific in it.
type probeHandler struct{}

func (probeHandler) Describe() sdk.Decl {
	assignedTarget := func(assigned string) *sdk.EventSemantics {
		return &sdk.EventSemantics{Target: &sdk.TargetSemantics{Assigned: json.RawMessage(assigned)}}
	}
	return sdk.Decl{
		Type: "probe", Kind: sdk.KindConnector,
		Events: []sdk.Event{
			{Name: "trusted", Semantics: assignedTarget("true")},
			{Name: "forged", Semantics: assignedTarget("false")},
			{Name: "maybe", Context: sdk.Schema{"ok": {Type: "boolean"}}, Semantics: assignedTarget(`"ok"`)},
			{Name: "no_target"}, // no target semantic at all: never warns
		},
	}
}
func (probeHandler) Invoke(sdk.InvokeRequest) (sdk.InvokeResult, error) {
	return sdk.InvokeResult{}, nil
}
func (probeHandler) StartSource(ctx context.Context, _ sdk.StartSourceRequest, _ func(any) error) error {
	<-ctx.Done()
	return nil
}

func init() { connector.RegisterInProcessConnector(probeHandler{}) }

func triggerWarnings(t *testing.T, y string) []string {
	t.Helper()
	cfg := loadConfig(t, y)
	reg := buildRegistry(t, cfg)
	return untrustedTargetWarnings(cfg, reg)
}

func TestUntrustedTargetWarningsFixturePlugin(t *testing.T) {
	cases := []struct {
		name      string
		on        string
		wantWarn  bool
		wantToken string
	}{
		{"statically assigned: no warning", "probe.trusted", false, ""},
		{"statically false: warns", "probe.forged", true, "declares `target.assigned: false`"},
		{"a fact: may be false, warns", "probe.maybe", true, `the fact "ok"`},
		{"no target semantics: no warning", "probe.no_target", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			warns := triggerWarnings(t, `
connectors:
  probe: { use: probe }
triggers:
  - on: `+c.on+`
    name: t1
    steps: []
`)
			if c.wantWarn && len(warns) == 0 {
				t.Fatalf("on: %s: expected a warning, got none", c.on)
			}
			if !c.wantWarn && len(warns) != 0 {
				t.Fatalf("on: %s: expected no warning, got %v", c.on, warns)
			}
			if c.wantToken != "" && !strings.Contains(warns[0], c.wantToken) {
				t.Fatalf("warning %q does not mention %q", warns[0], c.wantToken)
			}
		})
	}
}

func TestUntrustedTargetWarningsWebhook(t *testing.T) {
	// A static repo: is the operator's own word — assigned, no warning.
	warns := triggerWarnings(t, `
connectors:
  wh:
    use: webhook
    listen: ":0"
    sources:
      cw: { path: /h, repo: "acme/infra" }
triggers:
  - on: wh.cw
    name: t1
    steps: []
`)
	if len(warns) != 0 {
		t.Fatalf("a static repo: must not warn, got %v", warns)
	}

	// A body-templated repo: is sender-chosen — not assigned, warns.
	warns = triggerWarnings(t, `
connectors:
  wh:
    use: webhook
    listen: ":0"
    sources:
      cw: { path: /h, repo: "{{.body.owner}}/{{.body.name}}" }
triggers:
  - on: wh.cw
    name: t1
    steps: []
`)
	if len(warns) != 1 {
		t.Fatalf("a templated repo: must warn exactly once, got %v", warns)
	}
	if !strings.Contains(warns[0], `connector "wh" event "cw"`) {
		t.Fatalf("warning does not name the connector/event: %q", warns[0])
	}

	// A source with no repo: at all is synthetic, named after the operator's
	// own instance+source — still assigned, no warning.
	warns = triggerWarnings(t, `
connectors:
  wh:
    use: webhook
    listen: ":0"
    sources:
      cw: { path: /h }
triggers:
  - on: wh.cw
    name: t1
    steps: []
`)
	if len(warns) != 0 {
		t.Fatalf("a synthetic (no repo) source must not warn, got %v", warns)
	}
}

// A disabled/manual/abstract trigger never participates — matching every
// other static check in this file.
func TestUntrustedTargetWarningsSkipsManualAndAbstract(t *testing.T) {
	warns := triggerWarnings(t, `
connectors:
  wh:
    use: webhook
    listen: ":0"
    sources:
      cw: { path: /h, repo: "{{.body.owner}}/{{.body.name}}" }
triggers:
  - on: manual
    name: m1
    steps: []
  - on: wh.cw
    name: base
    abstract: true
    steps: []
`)
	if len(warns) != 0 {
		t.Fatalf("manual/abstract triggers must not warn, got %v", warns)
	}
}
