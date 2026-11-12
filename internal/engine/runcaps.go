package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
)

// runCap is one scope's policy.concurrency.max_runs: the runs of one trigger,
// of one connector's triggers, or of every trigger (global) that may be in
// flight at once. Waiters are served in arrival order.
type runCap struct {
	scope string // "trigger <name>", "connector <name>", "global"
	slots *slots
	// serial is a serialization key's pool (scope is the rendered key, by is
	// the trigger taking it); "" for a max_runs cap.
	serial string
	by     string
	rc     *runCaps
}

// runCaps holds each scope's pool, made on first use. The config is fixed for
// the engine's lifetime, so a pool's cap never changes.
//
// A serialization key's pool (a trigger's `serialize:`) is one slot per
// rendered key, shared by every trigger that renders the same key. Keys are
// per target, so their pools are counted and dropped once no run holds or
// waits on them.
type runCaps struct {
	mu      sync.Mutex
	pools   map[string]*slots
	serial  map[string]*serialPool
	holders map[string]string // serialization key → the trigger holding it
}

type serialPool struct {
	slots *slots
	refs  int // runs holding or waiting on it
}

// enterSerial returns key's pool, counting the caller in; leaveSerial counts
// it out.
func (rc *runCaps) enterSerial(key string) *slots {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.serial == nil {
		rc.serial = map[string]*serialPool{}
	}
	p, ok := rc.serial[key]
	if !ok {
		p = &serialPool{slots: newSlots(1)}
		rc.serial[key] = p
	}
	p.refs++
	return p.slots
}

func (rc *runCaps) leaveSerial(key string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if p, ok := rc.serial[key]; ok {
		if p.refs--; p.refs <= 0 {
			delete(rc.serial, key)
		}
	}
}

func (rc *runCaps) setHolder(key, by string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.holders == nil {
		rc.holders = map[string]string{}
	}
	if by == "" {
		delete(rc.holders, key)
		return
	}
	rc.holders[key] = by
}

func (rc *runCaps) holder(key string) string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.holders[key]
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

// runCapsOf is every cap a run of t under spec takes, in order: its
// serialization key first (it is per target, the narrowest), then
// runCapsFor's. A run that takes them must end with releaseRunCaps, or with
// acquireRunCaps failing.
func (e *Engine) runCapsOf(t core.Trigger, ref string, spec config.TriggerSpec) []runCap {
	caps := e.runCapsFor(ref, spec)
	if spec.Serialize == nil || spec.Serialize.Key == "" {
		return caps
	}
	key, err := flow.RenderKey(spec.Serialize.Key, e.flowBaseData(t))
	if err != nil {
		// A key that does not render serializes nothing rather than
		// lumping every target under one key. Logged once per trigger.
		if _, warned := e.groupWarn.LoadOrStore("serialize\x00"+ref, true); !warned {
			e.log("%s serialize key: %v — not serialized", tag(t), err)
		}
		return caps
	}
	name := spec.Name
	if name == "" {
		name = ref
	}
	if spec.PackInstance != "" && !strings.Contains(name, "/") {
		name = spec.PackInstance + "/" + name
	}
	c := runCap{scope: key, slots: e.runCaps.enterSerial(key), serial: key, by: name, rc: &e.runCaps}
	return append([]runCap{c}, caps...)
}

// acquireRunCaps takes a slot in each cap, in order, logging the wait when
// one is full. false: ctx ended first, and nothing is held.
func (e *Engine) acquireRunCaps(ctx context.Context, t core.Trigger, caps []runCap, prog *flow.Progress) bool {
	for i, c := range caps {
		if used, n := c.slots.usage(); used >= n {
			if c.serial != "" {
				holder := e.runCaps.holder(c.serial)
				e.log("%s waiting on %s (held by %s)", tag(t), c.serial, orUnknown(holder))
				if holder != "" {
					prog.Queued("a run of "+holder+" to finish", -1)
				} else {
					prog.Queued("another run to finish", -1)
				}
			} else {
				e.log("%s waiting for a run slot (%d/%d running, %s)", tag(t), used, n, c.scope)
				prog.Queued(fmt.Sprintf("a run slot (%d/%d running)", used, n), -1)
			}
		}
		if !c.slots.acquire(ctx, prioNormal) {
			releaseRunCaps(caps[:i])
			for _, rest := range caps[i:] {
				if rest.serial != "" {
					e.runCaps.leaveSerial(rest.serial)
				}
			}
			return false
		}
		if c.serial != "" {
			e.runCaps.setHolder(c.serial, c.by)
		}
	}
	return true
}

// tryAcquireCap takes c's slot only if it is free now.
func (e *Engine) tryAcquireCap(c runCap) bool {
	if !c.slots.tryAcquire() {
		return false
	}
	if c.serial != "" {
		e.runCaps.setHolder(c.serial, c.by)
	}
	return true
}

func orUnknown(s string) string {
	if s == "" {
		return "another run"
	}
	return s
}

func releaseRunCaps(caps []runCap) {
	for i := len(caps) - 1; i >= 0; i-- {
		caps[i].release()
	}
}

// release frees the cap's slot (a serialization key's holder is cleared,
// and its pool counted out).
func (c runCap) release() {
	if c.serial != "" && c.rc != nil {
		c.rc.setHolder(c.serial, "")
		c.slots.release()
		c.rc.leaveSerial(c.serial)
		return
	}
	c.slots.release()
}
