package engine

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestHandoffDoneInCLICapabilityCard pins the fix for the review hand-off that
// posted its review and then reported "no handoff.done tool available", leaving
// its workspace open. A background (interactive) hand-off is told by
// HandoffGuidance to "call the handoff.done tool", and its dispatch token is
// minted with handoff.done auto-granted (dispatch.EffectiveSkillPolicy). So the
// capability CARD the CLI agent reads must list handoff.done too — even when the
// step wrote no skill: block of its own. Before the fix the card was rendered
// from the raw skill: block and omitted the auto-grant, so advertisement and
// enforcement disagreed.
func TestHandoffDoneInCLICapabilityCard(t *testing.T) {
	var cfg config.Config
	// Empty config → the default built-in paseo runtime, a local shell → the
	// skill surface reaches the agent as the CLI card (prose), not native tools.
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New()})
	e := New(Options{
		Config: &cfg, Store: tempStore(t), Dispatch: &fakeDispatcher{}, Notifier: &fakeNotifier{},
		Flow: runner, Connectors: reg,
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil },
	})

	// A background hand-off with NO skill: block of its own still advertises
	// handoff.done, because that is what its token grants.
	bg := e.skillGuidance(config.Step{Background: true})
	if !strings.Contains(bg, "handoff.done") {
		t.Fatalf("background hand-off CLI card must list handoff.done; got: %q", bg)
	}

	// A non-hand-off step with no grant advertises nothing (guidance rides the
	// grant — unchanged by this fix).
	if plain := e.skillGuidance(config.Step{}); plain != "" {
		t.Fatalf("a step with no grant must get no skill guidance; got: %q", plain)
	}
}
