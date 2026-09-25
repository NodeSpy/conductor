package models

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

const v1 = "system_one/v1"

// A box with paseo (agent) and jev (decision-only, speaks v1). Both
// rosters come from the fake adapter.
func decisionBox(t *testing.T, fleets map[string]config.FleetSpec) *Resolver {
	t.Helper()
	withFakeCLI(t, map[string][]string{
		"paseo": {"claude-sonnet-5", "claude-opus-5"},
		"jev":   {"jev-1", "jev-0"},
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"paseo": {Default: true},
		"jev":   {},
	}, fleets)
	r := NewResolver(cfg, nil)
	r.DecisionOnly = func(name string) bool { return name == "jev" }
	r.NativeProtocols = func(name string) []string {
		if name == "jev" {
			return []string{v1}
		}
		return nil
	}
	return r
}

func labels(cs []Candidate) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		tag := ""
		if c.Native {
			tag = "*"
		}
		out[i] = tag + c.Label()
	}
	return strings.Join(out, ",")
}

// Native candidates come first, then agent candidates, each in fleet order.
func TestCandidatesRankNativeFirst(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "claude-sonnet-*", "jev-*"),
	})
	cs, err := r.Candidates(context.Background(), config.ModelSpecOf("light"), "", v1)
	if err != nil {
		t.Fatal(err)
	}
	// jev-* expands newest-first in roster order; the native group leads even
	// though the fleet lists sonnet first — a decision runtime answers the
	// protocol itself, which is the whole point of installing one.
	if got := labels(cs); got != "*jev/jev-1,*jev/jev-0,paseo/claude-sonnet-5" {
		t.Fatalf("candidates = %s", got)
	}
}

// The central guarantee: an AGENT step never resolves to a decision runtime,
// even when its fleet names that runtime's models first.
func TestAgentResolutionNeverPicksADecisionRuntime(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "jev-*", "claude-sonnet-*"),
	})
	d, err := r.Resolve(context.Background(), config.ModelSpecOf("light"), "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Runtime == "jev" || strings.HasPrefix(d.Model, "jev") {
		t.Fatalf("an agent step landed on the decision runtime: %+v", d)
	}
	if d.Model != "claude-sonnet-5" || d.Runtime != "paseo" {
		t.Fatalf("want the first agent-runnable model, got %+v", d)
	}
}

// Installing a decision runtime changes nothing until a fleet names its
// models: that listing is the consumer's opt-in.
func TestCandidatesWithoutOptInAreAgentOnly(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "claude-sonnet-*"),
	})
	cs, err := r.Candidates(context.Background(), config.ModelSpecOf("light"), "", v1)
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(cs); got != "paseo/claude-sonnet-5" {
		t.Fatalf("a fleet that doesn't list jev must never reach it: %s", got)
	}
	// A step with no model: at all never reaches a native runtime either.
	cs, err = r.Candidates(context.Background(), config.ModelSpec{}, "", v1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Native {
			t.Fatalf("no model: must not reach a decision runtime: %s", labels(cs))
		}
	}
}

// A native runtime must speak the step's protocol.
func TestCandidatesRequireTheProtocol(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "jev-*", "claude-sonnet-*"),
	})
	cs, err := r.Candidates(context.Background(), config.ModelSpecOf("light"), "", "system_one/v2")
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(cs); got != "paseo/claude-sonnet-5" {
		t.Fatalf("a runtime that doesn't speak v2 answers v2 only through the adapter path: %s", got)
	}
}

// A fleet only the decision runtime satisfies still ends in an agent last
// resort (Resolve's bare launch), so a Jev outage degrades instead of failing.
func TestCandidatesNativeOnlyFleetKeepsAnAgentLastResort(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "jev-*"),
	})
	cs, err := r.Candidates(context.Background(), config.ModelSpecOf("light"), "", v1)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) < 3 || !cs[0].Native || cs[len(cs)-1].Native {
		t.Fatalf("want native candidates then an agent last resort: %s", labels(cs))
	}
}

// Pinning the step to the decision runtime keeps it native-only; pinning it
// to paseo keeps Jev out.
func TestCandidatesRespectARuntimePin(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "jev-*", "claude-sonnet-*"),
	})
	cs, err := r.Candidates(context.Background(), config.ModelSpecOf("light"), "paseo", v1)
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(cs); got != "paseo/claude-sonnet-5" {
		t.Fatalf("runtime: paseo pins the step to paseo: %s", got)
	}
	cs, err = r.Candidates(context.Background(), config.ModelSpecOf("light"), "jev", v1)
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(cs); got != "*jev/jev-1,*jev/jev-0" {
		t.Fatalf("runtime: jev pins the step to jev: %s", got)
	}
}

// Excluded (the unsupported-model cache) applies to decide candidates too.
func TestCandidatesSkipExcludedModels(t *testing.T) {
	r := decisionBox(t, map[string]config.FleetSpec{
		"light": config.FleetOf(false, "jev-*", "claude-sonnet-*"),
	})
	r.Excluded = func(rt, m string) bool { return m == "jev-1" }
	cs, err := r.Candidates(context.Background(), config.ModelSpecOf("light"), "", v1)
	if err != nil {
		t.Fatal(err)
	}
	if got := labels(cs); got != "*jev/jev-0,paseo/claude-sonnet-5" {
		t.Fatalf("candidates = %s", got)
	}
}
