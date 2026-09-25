package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/decider"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/systemone"
)

type nopInvoker struct{}

func (nopInvoker) Invoke(context.Context, plugin.InvokeRequest) (map[string]any, error) {
	return nil, nil
}

// An agent step that PINS a decision runtime is refused at dispatch, loudly —
// resolution never picks one for an agent, so this is the only way to get
// there, and launching nothing would be a silent failure.
func TestAgentDispatchRefusesADecisionRuntime(t *testing.T) {
	eng, _, _, _ := buildFlowEngine(t, gateCfg)
	eng.SetDeciders(decider.Set{"jev": decider.New("jev", []string{systemone.ProtocolV1}, nopInvoker{}, nil)})
	_, err := eng.flowAgentServices().Dispatch(context.Background(), dispatch.Request{
		Action: config.Action{Type: "agent", Prompt: "fix it"},
		Step:   config.Step{Type: "agent", Runtime: "jev"},
	})
	if err == nil || !strings.Contains(err.Error(), "decision runtime") {
		t.Fatalf("want a refusal naming the decision runtime, got %v", err)
	}
	if !dispatch.IsUnrecoverable(err) {
		t.Fatal("the refusal must escalate, not retry — retrying cannot make a decision runtime launch an agent")
	}
}

// Installing deciders teaches the resolver which runtimes are decision-only,
// whichever of the two setters runs first.
func TestSetDecidersWiresTheResolver(t *testing.T) {
	set := decider.Set{"jev": decider.New("jev", []string{systemone.ProtocolV1}, nopInvoker{}, nil)}
	for name, order := range map[string]func(e *Engine, r *models.Resolver){
		"resolver first": func(e *Engine, r *models.Resolver) { e.SetModelResolver(r); e.SetDeciders(set) },
		"deciders first": func(e *Engine, r *models.Resolver) { e.SetDeciders(set); e.SetModelResolver(r) },
	} {
		t.Run(name, func(t *testing.T) {
			eng, _, _, _ := buildFlowEngine(t, gateCfg)
			res := models.NewResolver(&config.Config{}, nil)
			order(eng, res)
			if res.DecisionOnly == nil || !res.DecisionOnly("jev") || res.DecisionOnly("paseo") {
				t.Fatal("the resolver must learn exactly which runtimes are decision-only")
			}
			if p := res.NativeProtocols("jev"); len(p) != 1 || p[0] != systemone.ProtocolV1 {
				t.Fatalf("native protocols = %v", p)
			}
		})
	}
}
