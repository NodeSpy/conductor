package engine

import (
	"context"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// runCap is one scope's policy.concurrency.max_runs: the runs of one trigger,
// of one connector's triggers, or of every trigger (global) that may be in
// flight at once. Waiters are served in arrival order.
type runCap struct {
	scope string // "trigger <name>", "connector <name>", "global"
	slots *slots
}

// runCaps holds each scope's pool, made on first use. The config is fixed for
// the engine's lifetime, so a pool's cap never changes.
type runCaps struct {
	mu    sync.Mutex
	pools map[string]*slots
}

func (rc *runCaps) pool(key string, n int) *slots {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.pools == nil {
		rc.pools = map[string]*slots{}
	}
	s, ok := rc.pools[key]
	if !ok {
		s = newSlots(n)
		rc.pools[key] = s
	}
	return s
}

// runCapsFor is every max_runs that governs spec's runs, narrowest first: the
// order every run takes them in, so two runs never hold one each while waiting
// on the other's.
func (e *Engine) runCapsFor(ref string, spec config.TriggerSpec) []runCap {
	var out []runCap
	add := func(key, scope string, p *config.Policy) {
		if n := p.MaxRuns(); n > 0 {
			out = append(out, runCap{scope: scope, slots: e.runCaps.pool(key, n)})
		}
	}
	name := spec.Name
	if name == "" {
		name = ref
	}
	add("trigger\x00"+name, "trigger "+name, spec.Policy)
	if c := spec.Connector(); c != "" {
		if r, ok := e.cfg.ConnectorsMap[c]; ok {
			add("connector\x00"+c, "connector "+c, r.Policy)
		}
	}
	add("global", "global", e.cfg.Policy)
	return out
}

// acquireRunCaps takes a slot in each cap, in order, logging the wait when
// one is full. false: ctx ended first, and nothing is held.
func (e *Engine) acquireRunCaps(ctx context.Context, t core.Trigger, caps []runCap) bool {
	for i, c := range caps {
		if used, n := c.slots.usage(); used >= n {
			e.log("%s waiting for a run slot (%d/%d running, %s)", tag(t), used, n, c.scope)
		}
		if !c.slots.acquire(ctx, prioNormal) {
			releaseRunCaps(caps[:i])
			return false
		}
	}
	return true
}

func releaseRunCaps(caps []runCap) {
	for i := len(caps) - 1; i >= 0; i-- {
		caps[i].slots.release()
	}
}
