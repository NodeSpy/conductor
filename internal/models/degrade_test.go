package models

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// These cover the three ways discovery used to fail CLOSED and take dispatch
// down with it: a poisoned negative cache, a bare launch that names no
// provider, and a diagnostic that threw away the reason.

// countingLister reports how many times a runtime was actually probed.
type countingLister struct {
	n      *atomic.Int32
	roster Roster
	err    error
}

func (c *countingLister) List(context.Context) (Roster, error) {
	c.n.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.roster, nil
}

// withCountingCLI registers an adapter whose answer can flip mid-test, so a
// runtime that fails once and recovers can be observed.
func withCountingCLI(t *testing.T, n *atomic.Int32, answer func() (Roster, error)) {
	t.Helper()
	registryMu.RLock()
	prev, had := registry["cli"]
	registryMu.RUnlock()
	Register("cli", func(Runtime, *Catalog) Lister {
		r, err := answer()
		return &countingLister{n: n, roster: r, err: err}
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

// A discovery failure used to be remembered FOREVER: one blip and the runtime
// enumerated nothing for the life of the process, so every fleet matched
// nothing and every dispatch degraded to a bare launch until conductor was
// restarted. The failure must expire.
func TestDiscoveryFailureExpiresAndTheRuntimeIsProbedAgain(t *testing.T) {
	var probes atomic.Int32
	var healthy atomic.Bool
	withCountingCLI(t, &probes, func() (Roster, error) {
		if healthy.Load() {
			return Roster{{ID: "claude-sonnet-5", Provider: "claude"}}, nil
		}
		return nil, errors.New("daemon not running")
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"box": {}}, nil)
	now := time.Now()
	r := NewResolver(cfg, nil)
	r.now = func() time.Time { return now }

	ctx := context.Background()
	if got := r.roster(ctx, "box"); got != nil {
		t.Fatalf("expected the failing probe to yield nothing, got %v", got.IDs())
	}
	// Within the TTL the failure is cached — no second probe.
	r.roster(ctx, "box")
	if probes.Load() != 1 {
		t.Fatalf("probed %d times inside the TTL, want 1", probes.Load())
	}

	// The runtime recovers, but conductor will not notice until the TTL is up.
	healthy.Store(true)
	r.roster(ctx, "box")
	if probes.Load() != 1 {
		t.Fatalf("probed %d times inside the TTL, want 1", probes.Load())
	}

	now = now.Add(FailureTTL + time.Minute)
	got := r.roster(ctx, "box")
	if probes.Load() != 2 {
		t.Fatalf("probed %d times after the TTL, want 2", probes.Load())
	}
	if len(got) != 1 || got[0].ID != "claude-sonnet-5" {
		t.Fatalf("recovered roster = %v, want claude-sonnet-5", got.IDs())
	}
}

// A SUCCESSFUL roster is still cached for the life of the process — only the
// failure expires.
func TestSuccessfulDiscoveryIsNotReprobedAfterTheFailureTTL(t *testing.T) {
	var probes atomic.Int32
	withCountingCLI(t, &probes, func() (Roster, error) {
		return Roster{{ID: "claude-sonnet-5", Provider: "claude"}}, nil
	})
	cfg := testCfg(t, map[string]config.RuntimeConfig{"box": {}}, nil)
	now := time.Now()
	r := NewResolver(cfg, nil)
	r.now = func() time.Time { return now }

	r.roster(context.Background(), "box")
	now = now.Add(10 * FailureTTL)
	r.roster(context.Background(), "box")
	if probes.Load() != 1 {
		t.Fatalf("probed %d times, want 1 — a good roster should not expire", probes.Load())
	}
}

// A bare launch means "no --model", NOT "no --provider". paseo rejects a run
// naming neither with MISSING_PROVIDER, so an unqualified bare launch is not
// a safe degrade on that backend — it is a guaranteed failure. When the fleet
// matches nothing but the roster is live, name a provider off it.
func TestBareLaunchAfterAnUnmatchedFleetStillNamesAProvider(t *testing.T) {
	withFakeCLIRoster(t, map[string]Roster{
		"box": {{ID: "claude-opus-5", Provider: "claude"}},
	})
	cfg := testCfg(t,
		map[string]config.RuntimeConfig{"box": {}},
		map[string]config.FleetSpec{"light": {Any: []string{"gpt-5-mini"}}})

	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("light"), "")
	if !d.Bare {
		t.Fatalf("expected a bare launch, got model %q", d.Model)
	}
	if d.Model != "" {
		t.Errorf("bare launch must pass no model, got %q", d.Model)
	}
	if d.Provider != "claude" {
		t.Fatalf("bare launch provider = %q, want claude", d.Provider)
	}
}

// An explicit models.provider: pins the fallback — the knob for a box whose
// discovery cannot be relied on. It wins over whatever the roster says.
func TestBareLaunchProviderPinWinsOverTheRoster(t *testing.T) {
	withFakeCLIRoster(t, map[string]Roster{
		"box": {{ID: "claude-opus-5", Provider: "claude"}},
	})
	cfg := testCfg(t,
		map[string]config.RuntimeConfig{"box": {Models: &config.RuntimeModels{Provider: "codex"}}},
		map[string]config.FleetSpec{"light": {Any: []string{"gpt-5-mini"}}})

	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("light"), "")
	if d.Provider != "codex" {
		t.Fatalf("provider = %q, want the pinned codex", d.Provider)
	}
}

// The pin also rescues the case the roster cannot: discovery is down, so
// there is nothing to derive a provider from.
func TestBareLaunchProviderPinAppliesWhenDiscoveryIsDown(t *testing.T) {
	withFakeCLIRoster(t, nil) // every runtime returns ErrNoDiscovery
	cfg := testCfg(t,
		map[string]config.RuntimeConfig{"box": {Models: &config.RuntimeModels{Provider: "claude"}}}, nil)

	// No model declared at all — the other bare path.
	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpec{}, "")
	if !d.Bare || d.Provider != "claude" {
		t.Fatalf("bare=%v provider=%q, want bare with claude", d.Bare, d.Provider)
	}
}

// With neither a roster nor a pin there is genuinely nothing to name, and the
// notice has to say so — that is the ahead-of-time MISSING_PROVIDER warning.
func TestBareLaunchWithNoProviderSaysSo(t *testing.T) {
	withFakeCLIRoster(t, nil)
	cfg := testCfg(t, map[string]config.RuntimeConfig{"box": {}}, nil)

	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpec{}, "")
	if d.Provider != "" {
		t.Fatalf("provider = %q, want empty", d.Provider)
	}
	if !strings.Contains(d.Notice, "MISSING_PROVIDER") {
		t.Errorf("notice does not warn about MISSING_PROVIDER: %q", d.Notice)
	}
	if !strings.Contains(d.Notice, "models.provider") {
		t.Errorf("notice does not name the knob that fixes it: %q", d.Notice)
	}
}

// "no configured runtime could enumerate its models" with the cause discarded
// is what made the live outage unreadable. The reason must reach the notice.
func TestUnmatchedFleetNoticeCarriesTheDiscoveryFailure(t *testing.T) {
	var probes atomic.Int32
	withCountingCLI(t, &probes, func() (Roster, error) {
		return nil, errors.New("DAEMON_NOT_RUNNING: no daemon at home /srv/paseo")
	})
	cfg := testCfg(t,
		map[string]config.RuntimeConfig{"box": {}},
		// More than one acceptable entry, so this is not an exact pin (which
		// passes through unconfirmed when nothing can enumerate).
		map[string]config.FleetSpec{"light": {Any: []string{"gpt-5-mini", "gemini-*-flash"}}})

	d := mustResolve(t, NewResolver(cfg, nil), config.ModelSpecOf("light"), "")
	if !strings.Contains(d.Notice, "DAEMON_NOT_RUNNING") {
		t.Errorf("notice threw away the reason: %q", d.Notice)
	}
	if !strings.Contains(d.Notice, "box") {
		t.Errorf("notice does not name the runtime that failed: %q", d.Notice)
	}
}

// The `home:` a paseo runtime configures must reach discovery. Before this,
// config carried it, dispatch used it, and the lister never saw it.
func TestRuntimeOfCarriesHomeToDiscovery(t *testing.T) {
	got := runtimeOf("paseo", config.RuntimeConfig{Use: "paseo", Home: "/srv/paseo"})
	if got.Home != "/srv/paseo" {
		t.Fatalf("Runtime.Home = %q, want /srv/paseo", got.Home)
	}
}
