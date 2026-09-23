package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/models"
)

type noDiscovery struct{}

func (noDiscovery) List(context.Context) (models.Roster, error) { return nil, models.ErrNoDiscovery }

type fixedLister struct{ ids []string }

func (f fixedLister) List(context.Context) (models.Roster, error) {
	r := make(models.Roster, 0, len(f.ids))
	for _, id := range f.ids {
		r = append(r, models.Model{ID: id})
	}
	return r, nil
}

// THE INCIDENT REGRESSION (opus-5-5 vs claude-code 2.1.220): a provider
// refusing the fleet's newest model at RUN time must not fail the step — the
// dispatch walks the fleet to the next model that runs.
func TestDispatchAgentFallsBackThroughFleetOnModelUnsupported(t *testing.T) {
	models.Register("cli", func(models.Runtime, *models.Catalog) models.Lister {
		return fixedLister{ids: []string{"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8"}}
	})
	t.Cleanup(func() {
		models.Register("cli", func(models.Runtime, *models.Catalog) models.Lister { return noDiscovery{} })
	})

	cfg := baseCfg()
	cfg.Runtimes = map[string]config.RuntimeConfig{"main": {Use: "cli", Tool: "claude-code"}}
	cfg.Models = map[string]config.FleetSpec{"heavy": config.FleetOf(false, "claude-opus-*")}

	var dispatched []string
	d := &fakeDispatcher{onDispatch: func(req dispatch.Request) (dispatch.RunRef, error) {
		dispatched = append(dispatched, req.Model)
		if req.Model == "claude-opus-5-5" {
			return dispatch.RunRef{}, dispatch.ErrModelUnsupported
		}
		return dispatch.RunRef{AgentID: "a1", Output: `{"decision":"approve"}`}, nil
	}}
	e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
	res := models.NewResolver(cfg, nil)
	unsup := models.NewUnsupportedCache("")
	res.Excluded = unsup.Has
	e.SetModelResolver(res)
	e.SetModelFallback(unsup)

	step := config.Step{Name: "review", Model: config.ModelSpecOf("heavy")}
	req := dispatch.Request{Step: step, Model: "claude-opus-5-5", Provider: "claude"}
	ref, err := e.dispatchAgent(context.Background(), d, req)
	if err != nil {
		t.Fatalf("fallback should recover the dispatch: %v", err)
	}
	if len(dispatched) != 2 || dispatched[0] != "claude-opus-5-5" || dispatched[1] != "claude-opus-5" {
		t.Fatalf("expected one refusal then the next fleet candidate, got %v", dispatched)
	}
	if ref.AgentID != "a1" {
		t.Fatalf("fallback dispatch's ref should win: %+v", ref)
	}
	if !unsup.Has("main", "claude-opus-5-5") {
		t.Fatal("the refused model must be marked unsupported")
	}
}

// Fleet exhausted: every candidate refused → the typed error surfaces (with
// the audit trail), never an infinite loop.
func TestDispatchAgentFallbackExhaustsFleet(t *testing.T) {
	models.Register("cli", func(models.Runtime, *models.Catalog) models.Lister {
		return fixedLister{ids: []string{"m1", "m2"}}
	})
	t.Cleanup(func() {
		models.Register("cli", func(models.Runtime, *models.Catalog) models.Lister { return noDiscovery{} })
	})
	cfg := baseCfg()
	cfg.Runtimes = map[string]config.RuntimeConfig{"main": {Use: "cli", Tool: "claude-code"}}
	cfg.Models = map[string]config.FleetSpec{"f": config.FleetOf(false, "m*")}

	calls := 0
	d := &fakeDispatcher{onDispatch: func(req dispatch.Request) (dispatch.RunRef, error) {
		calls++
		return dispatch.RunRef{}, dispatch.ErrModelUnsupported
	}}
	e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
	res := models.NewResolver(cfg, nil)
	unsup := models.NewUnsupportedCache("")
	res.Excluded = unsup.Has
	e.SetModelResolver(res)
	e.SetModelFallback(unsup)

	req := dispatch.Request{Step: config.Step{Name: "s", Model: config.ModelSpecOf("f")}, Model: "m1"}
	_, err := e.dispatchAgent(context.Background(), d, req)
	if !errors.Is(err, dispatch.ErrModelUnsupported) {
		t.Fatalf("exhausted fleet must surface the typed error, got %v", err)
	}
	if calls < 2 || calls > 5 {
		t.Fatalf("bounded attempts expected, got %d", calls)
	}
}
