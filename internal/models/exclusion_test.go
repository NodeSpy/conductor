package models

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// The fleet-fallback core: a model marked unsupported is skipped on
// re-resolution and the NEXT fleet candidate wins — and when the mark expires,
// the newest model is chosen again.
func TestResolveSkipsExcludedModels(t *testing.T) {
	withFakeCLI(t, map[string][]string{
		"main": {"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8"},
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{
		"main": {},
	}, map[string]config.FleetSpec{
		"heavy": config.FleetOf(false, "claude-opus-*"),
	})

	unsup := NewUnsupportedCache("")
	r := NewResolver(cfg, nil)
	r.Excluded = unsup.Has

	if d := mustResolve(t, r, config.ModelSpecOf("heavy"), ""); d.Model != "claude-opus-5-5" {
		t.Fatalf("unexcluded resolve should pick the newest roster match, got %q", d.Model)
	}
	// The provider refuses opus-5-5 (client too old): mark → next candidate.
	unsup.Mark("", "claude-opus-5-5")
	if d := mustResolve(t, r, config.ModelSpecOf("heavy"), ""); d.Model != "claude-opus-5" {
		t.Fatalf("excluded model must fall through to the next candidate, got %q", d.Model)
	}
	unsup.Mark("", "claude-opus-5")
	if d := mustResolve(t, r, config.ModelSpecOf("heavy"), ""); d.Model != "claude-opus-4-8" {
		t.Fatalf("second exclusion must fall through again, got %q", d.Model)
	}
}
