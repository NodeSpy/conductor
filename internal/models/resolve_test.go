package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// The resolution ladder is exercised against FIXED rosters — a fake adapter
// registered under a test-only implementation name — so these tests assert
// the ladder, not discovery (which models_test.go covers).

type fakeLister struct {
	roster Roster
	err    error
}

func (f *fakeLister) List(context.Context) (Roster, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.roster, nil
}

// testCfg builds a config whose runtimes all resolve to the builtin "cli"
// implementation, which withFakeCLI swaps for a fixed-roster adapter.
func testCfg(t *testing.T, runtimes map[string]config.RuntimeConfig, fleets map[string]config.FleetSpec) *config.Config {
	t.Helper()
	set := config.RuntimeSet{}
	for n, r := range runtimes {
		if r.Use == "" {
			r.Use = "cli"
		}
		set[n] = r
	}
	return &config.Config{Runtimes: set, Models: fleets}
}

// The fake adapter is registered for the builtin "cli" implementation name so
// a plain `use: cli` runtime picks it up. Saved/restored per test.
func withFakeCLI(t *testing.T, byRuntime map[string][]string) {
	t.Helper()
	registryMu.RLock()
	prev, had := registry["cli"]
	registryMu.RUnlock()
	Register("cli", func(rt Runtime, _ *Catalog) Lister {
		ids, ok := byRuntime[rt.Name]
		if !ok {
			return &fakeLister{err: ErrNoDiscovery}
		}
		r := make(Roster, 0, len(ids))
		for _, id := range ids {
			r = append(r, Model{ID: id})
		}
		return &fakeLister{roster: r}
	})
	t.Cleanup(func() {
		registryMu.Lock()
		if had {
			registry["cli"] = prev
		} else {
			delete(registry, "cli")
		}
		registryMu.Unlock()
	})
}

func mustResolve(t *testing.T, r *Resolver, spec config.ModelSpec, hint string) Decision {
	t.Helper()
	d, err := r.Resolve(context.Background(), spec, hint)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return d
}

// --- rung 2: intersect + prefer -------------------------------------------

func TestResolveFleetIntersectsRosterAndRanksByPrefer(t *testing.T) {
	withFakeCLI(t, map[string][]string{
		"main": {"claude-opus-5", "claude-sonnet-5", "gpt-5.6-sol"},
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"main": {Models: &config.RuntimeModels{Prefer: []string{"gpt-5.6-*"}}},
	}, map[string]config.FleetSpec{
		"reviewer": config.FleetOf(false, "claude-opus-*", "gpt-5.6-*"),
	})

	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("reviewer"), "")
	// The fleet lists claude first, but the CONSUMER prefers gpt — the
	// consumer disposes.
	if d.Model != "gpt-5.6-sol" || d.Runtime != "main" || d.Bare {
		t.Fatalf("decision = %#v", d)
	}
	if !strings.Contains(d.Reason, "reviewer") {
		t.Errorf("reason = %q", d.Reason)
	}
}

func TestResolveFleetOrderWinsWithoutPrefer(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"gpt-5.6-sol", "claude-opus-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"reviewer": config.FleetOf(false, "claude-opus-*", "gpt-5.6-*"),
	})
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("reviewer"), "")
	if d.Model != "claude-opus-5" {
		t.Fatalf("decision = %#v", d)
	}
}

func TestResolveInlineArrayAndObjectForms(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5", "gpt-5.6-sol"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	r := NewResolver(cfg, nil)

	var arr config.ModelSpec
	if err := unmarshalSpec(&arr, `[gpt-5.6-*, claude-opus-*]`); err != nil {
		t.Fatal(err)
	}
	if d := mustResolve(t, r, arr, ""); d.Model != "gpt-5.6-sol" {
		t.Errorf("array form = %#v", d)
	}
	if d := mustResolve(t, r, config.FleetOf(true, "claude-opus-*"), ""); d.Model != "claude-opus-5" {
		t.Errorf("object form = %#v", d)
	}
}

func TestResolveWildcardStarPicksConcreteModelViaPrefer(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-sonnet-5", "claude-opus-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"main": {Models: &config.RuntimeModels{Prefer: []string{"claude-opus-*"}}},
	}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("*"), "")
	// `"*"` is NOT bare launch: it resolves to a concrete model.
	if d.Bare {
		t.Fatal(`model: "*" must not bare-launch`)
	}
	if d.Model != "claude-opus-5" {
		t.Fatalf("decision = %#v", d)
	}
}

func TestResolveRespectsRuntimeAllow(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5", "gpt-5.6-sol"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"main": {Models: &config.RuntimeModels{Allow: []string{"claude-*"}}},
	}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("*"), "")
	if d.Model != "claude-opus-5" {
		t.Fatalf("allow: should have excluded gpt: %#v", d)
	}
	// And an explicitly excluded model is simply not available.
	d = mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("gpt-5.6-sol"), "")
	if !d.Bare {
		t.Fatalf("an allow-excluded pin should fall through: %#v", d)
	}
}

func TestResolvePicksTheRuntimeThatOffersTheModel(t *testing.T) {
	withFakeCLI(t, map[string][]string{
		"anthropic-box": {"claude-opus-5"},
		"openai-box":    {"gpt-5.6-sol"},
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"anthropic-box": {}, "openai-box": {},
	}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("gpt-5.6-sol"), "")
	if d.Runtime != "openai-box" || d.Model != "gpt-5.6-sol" {
		t.Fatalf("decision = %#v", d)
	}
}

func TestResolveTieBrokenByDefaultRuntimeThenName(t *testing.T) {
	withFakeCLI(t, map[string][]string{"a-box": {"claude-opus-5"}, "z-box": {"claude-opus-5"}})

	// Both offer it, neither is default → name order.
	cfg := testCfg(t, map[string]config.RuntimeConfig{"a-box": {}, "z-box": {}}, nil)
	if d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("claude-opus-5"), ""); d.Runtime != "a-box" {
		t.Errorf("name tiebreak: %#v", d)
	}
	// default: true wins over name order.
	cfg = testCfg(t, map[string]config.RuntimeConfig{"a-box": {}, "z-box": {Default: true}}, nil)
	if d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("claude-opus-5"), ""); d.Runtime != "z-box" {
		t.Errorf("default tiebreak: %#v", d)
	}
}

func TestResolveRuntimeHintPinsTheSearch(t *testing.T) {
	withFakeCLI(t, map[string][]string{
		"anthropic-box": {"claude-opus-5"},
		"openai-box":    {"gpt-5.6-sol"},
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"anthropic-box": {}, "openai-box": {}}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("*"), "openai-box")
	if d.Model != "gpt-5.6-sol" || d.Runtime != "openai-box" {
		t.Fatalf("decision = %#v", d)
	}
}

// --- rung 1: consumer override --------------------------------------------

func TestResolveConsumerOverrideBeatsTheFleet(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5", "gpt-5.6-sol"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"reviewer": config.FleetOf(true, "gpt-5.6-*"),
	})
	r := NewResolver(cfg, nil)
	r.Overrides = map[string]string{"reviewer": "claude-opus-5"}

	d := mustResolve(t, r, config.ModelSpecOf("reviewer"), "")
	if d.Model != "claude-opus-5" {
		t.Fatalf("override ignored: %#v", d)
	}
	if !strings.Contains(d.Reason, "override") {
		t.Errorf("reason = %q", d.Reason)
	}
	if d.Runtime != "main" {
		t.Errorf("runtime = %q", d.Runtime)
	}
}

func TestResolveOverrideOnlyAppliesToNamedFleets(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5", "gpt-5.6-sol"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	r := NewResolver(cfg, nil)
	// An override keyed by something the step did not name as a fleet must
	// not hijack an inline spec.
	r.Overrides = map[string]string{"gpt-5.6-sol": "claude-opus-5"}
	if d := mustResolve(t, r, config.ModelSpecOf("gpt-5.6-sol"), ""); d.Model != "gpt-5.6-sol" {
		t.Fatalf("inline spec was overridden: %#v", d)
	}
}

// --- rung 3: bare launch ---------------------------------------------------

func TestResolveNoModelBareLaunches(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpec{}, "")
	if !d.Bare || d.Model != "" {
		t.Fatalf("decision = %#v", d)
	}
}

func TestResolveNoModelUsesRuntimeDefaultThenPrefer(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-sonnet-5", "claude-opus-5"}})

	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"main": {Models: &config.RuntimeModels{Default: "claude-sonnet-5"}},
	}, nil)
	if d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpec{}, ""); d.Model != "claude-sonnet-5" || d.Bare {
		t.Fatalf("models.default ignored: %#v", d)
	}

	// No default, but a prefer: that the roster confirms.
	cfg = testCfg(t, map[string]config.RuntimeConfig{
		"main": {Models: &config.RuntimeModels{Prefer: []string{"claude-opus-*"}}},
	}, nil)
	if d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpec{}, ""); d.Model != "claude-opus-5" {
		t.Fatalf("prefer: should supply the effective default: %#v", d)
	}

	// A prefer: nothing in the roster satisfies falls back to bare launch.
	cfg = testCfg(t, map[string]config.RuntimeConfig{
		"main": {Models: &config.RuntimeModels{Prefer: []string{"llama-*"}}},
	}, nil)
	if d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpec{}, ""); !d.Bare {
		t.Fatalf("unsatisfiable prefer: should bare-launch: %#v", d)
	}
}

func TestResolveUnsatisfiableOptionalFleetBareLaunchesWithANotice(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"summarizer": config.FleetOf(false, "llama-*", "mistral-*"),
	})
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("summarizer"), "")
	if !d.Bare {
		t.Fatalf("decision = %#v", d)
	}
	if !strings.Contains(d.Notice, "summarizer") || !strings.Contains(d.Notice, "claude-opus-5") {
		t.Errorf("notice should name the fleet and what IS available: %q", d.Notice)
	}
}

// Bare launch and `"*"` are different outcomes against the same roster.
func TestBareLaunchIsDistinctFromStarWildcard(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	r := NewResolver(cfg, nil)

	star := mustResolve(t, r, config.ModelSpecOf("*"), "")
	none := mustResolve(t, r, config.ModelSpec{}, "")
	if star.Bare || star.Model == "" {
		t.Errorf(`"*" must resolve to a concrete model: %#v`, star)
	}
	if !none.Bare || none.Model != "" {
		t.Errorf("no model: must bare-launch: %#v", none)
	}
}

// --- rung 4: required --------------------------------------------------------

func TestResolveRequiredFleetWithNoMatchIsAHardError(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-haiku-4-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"reviewer": config.FleetOf(true, "claude-opus-*", "gpt-5.6-*"),
	})
	_, err := NewResolver(cfg, nil).Resolve(context.Background(), config.ModelSpecOf("reviewer"), "")
	if !errors.Is(err, ErrNoAcceptableModel) {
		t.Fatalf("want ErrNoAcceptableModel, got %v", err)
	}
	// The message must name both sides so the operator can act on it.
	for _, want := range []string{"reviewer", "claude-opus-*", "claude-haiku-4-5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestResolveRequiredStarIsOnlyUnsatisfiableWithAnEmptyRoster(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"anything-at-all"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"any": config.FleetOf(true, "*"),
	})
	if d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("any"), ""); d.Model != "anything-at-all" {
		t.Fatalf("decision = %#v", d)
	}
}

// --- discovery unavailable ------------------------------------------------

func TestExactPinPassesThroughWhenNothingCanEnumerate(t *testing.T) {
	withFakeCLI(t, nil) // no runtime enumerates
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("some-private-model"), "")
	if d.Bare || d.Model != "some-private-model" {
		t.Fatalf("an exact pin must survive an un-enumerable runtime: %#v", d)
	}
}

func TestPinIsNotPassedThroughWhenARosterSaysNo(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-opus-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("claude-opus-9"), "")
	if !d.Bare {
		t.Fatalf("a roster that answered should not be second-guessed: %#v", d)
	}
	if !strings.Contains(d.Notice, "claude-opus-9") {
		t.Errorf("notice should name the unavailable pin: %q", d.Notice)
	}
}

func TestWildcardNeverConjuresAModelWhenNothingEnumerates(t *testing.T) {
	withFakeCLI(t, nil)
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("claude-opus-*"), "")
	if !d.Bare {
		t.Fatalf("a wildcard with no roster must bare-launch, not guess: %#v", d)
	}
}

// --- CheckRequired ---------------------------------------------------------

func TestCheckRequiredReportsTheOffendingStep(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-haiku-4-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"reviewer": config.FleetOf(true, "claude-opus-*"),
	})
	cfg.Triggers = []config.TriggerSpec{{
		Name: "review", On: "gh.pull_request",
		Steps: []config.Step{{ID: "security", Type: "agent", Model: config.ModelSpecOf("reviewer")}},
	}}
	err := NewResolver(cfg, nil).CheckRequired(context.Background())
	if err == nil || !strings.Contains(err.Error(), `trigger "review" security`) {
		t.Fatalf("want the step named, got %v", err)
	}
}

// The degraded-boot invariant: a box that cannot reach its providers has not
// learned that a model is unavailable — it has learned nothing. Turning that
// into a hard failure would crash-loop an auto-updating fleet.
func TestCheckRequiredIsSilentWhenNothingCanEnumerate(t *testing.T) {
	withFakeCLI(t, nil)
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"reviewer": config.FleetOf(true, "claude-opus-*"),
	})
	cfg.Triggers = []config.TriggerSpec{{
		Name: "review", On: "gh.pull_request",
		Steps: []config.Step{{ID: "security", Type: "agent", Model: config.ModelSpecOf("reviewer")}},
	}}
	if err := NewResolver(cfg, nil).CheckRequired(context.Background()); err != nil {
		t.Fatalf("undiscoverable must not fail the check: %v", err)
	}
}

func TestCheckRequiredIgnoresOptionalFleets(t *testing.T) {
	withFakeCLI(t, map[string][]string{"main": {"claude-haiku-4-5"}})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, map[string]config.FleetSpec{
		"summarizer": config.FleetOf(false, "claude-opus-*"),
	})
	cfg.Triggers = []config.TriggerSpec{{
		Name: "s", On: "gh.pull_request",
		Steps: []config.Step{{ID: "sum", Type: "agent", Model: config.ModelSpecOf("summarizer")}},
	}}
	if err := NewResolver(cfg, nil).CheckRequired(context.Background()); err != nil {
		t.Fatalf("optional fleets never hard-fail: %v", err)
	}
}

// --- memoization -----------------------------------------------------------

func TestRosterIsDiscoveredOncePerRuntime(t *testing.T) {
	calls := 0
	registryMu.RLock()
	prev, had := registry["cli"]
	registryMu.RUnlock()
	Register("cli", func(Runtime, *Catalog) Lister {
		return listerFunc(func() (Roster, error) {
			calls++
			return Roster{{ID: "claude-opus-5"}}, nil
		})
	})
	t.Cleanup(func() {
		registryMu.Lock()
		if had {
			registry["cli"] = prev
		} else {
			delete(registry, "cli")
		}
		registryMu.Unlock()
	})

	cfg := testCfg(t, map[string]config.RuntimeConfig{"main": {}}, nil)
	r := NewResolver(cfg, nil)
	for i := 0; i < 5; i++ {
		mustResolve(t, r, config.ModelSpecOf("*"), "")
	}
	if calls != 1 {
		t.Fatalf("discovered %d times, want 1", calls)
	}
}

type listerFunc func() (Roster, error)

func (f listerFunc) List(context.Context) (Roster, error) { return f() }

// unmarshalSpec parses a YAML fragment into a ModelSpec (the array/object
// forms have no exported constructor for the non-required case).
func unmarshalSpec(dst *config.ModelSpec, src string) error {
	var s struct {
		Model config.ModelSpec `yaml:"model"`
	}
	if err := yaml.Unmarshal([]byte("model: "+src+"\n"), &s); err != nil {
		return err
	}
	*dst = s.Model
	return nil
}
