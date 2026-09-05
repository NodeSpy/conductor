package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Session affinity (#36 §10). By default each dispatch gets a fresh agent;
// an agent profile with a `session:` block instead binds a live session to
// the rendered key, and every event resolving to that key — across ALL
// triggers dispatching to that agent — reaches the same session as a
// follow-up prompt with full prior context. One agent per PR, shared by a
// comment, a check failure, and a review-change alike.
//
// The registry is keyed by (agent, key-value), serialized per key (at most
// one prompt in flight; concurrent same-key events queue on the key's lock —
// the `group:` one-run-per-key guarantee extended across the session's
// life), persisted in conductor's own state (affinity.json beside audit/
// dedup/holds), and resumed after a restart via the runtime's native session
// handle (paseo re-binds the agent id; ACP session/load). Sessions are
// evicted idle past idle_ttl, older than max_lifetime, or on an `end_on`
// lifecycle event; the next event starts fresh.
//
// Only session-persistent runtimes participate (paseo with a follow-up
// sender, ACP); one-shot runtimes fall back to fresh-per-event and lean on
// shared memory (§9) for continuity.

// AffinityRef is one persisted (agent, key) → session binding.
type AffinityRef struct {
	Agent      string    // agent profile name
	Key        string    // rendered session key value
	Controller string    // runtime that owns the session
	SessionID  string    // the runtime's session/agent id
	Created    time.Time // spawn time (max_lifetime)
	LastUsed   time.Time // last prompt (idle_ttl)
}

// AffinityStore persists the bindings in conductor's own state. *store.Store
// satisfies it; tests inject an in-memory fake.
type AffinityStore interface {
	PutAffinity(AffinityRef) error
	DeleteAffinity(agent, key string) error
	Affinities() []AffinityRef
}

// SessionPersistent is the capability a controller implements when its
// sessions survive across dispatches (and restarts) by id. Controllers
// without it get fresh-per-event dispatch even for session: profiles.
type SessionPersistent interface {
	SessionPersistent() bool
}

// SupportsSessionPersistence reports whether c can host keyed sessions.
func SupportsSessionPersistence(c Controller) bool {
	if sp, ok := c.(SessionPersistent); ok {
		return sp.SessionPersistent()
	}
	return false
}

// Affinity owns the global (agent, key) → session registry.
type Affinity struct {
	reg   *Registry
	store AffinityStore
	cfg   *config.Config
	// hold/release manage the reaper's "never reap" set for bound agents: a
	// keyed session must idle between events without being archived.
	hold    func(id string)
	release func(id string)
	log     func(string, ...any)
	now     func() time.Time

	mu    sync.Mutex
	refs  map[string]AffinityRef // bindingKey → persisted ref
	live  map[string]Session     // bindingKey → live session held by THIS process
	locks map[string]*sync.Mutex // bindingKey → serialization lock
	owned map[string]string      // sessionID → bindingKey (archive guards)
}

// NewAffinity builds the registry, restoring persisted bindings (and
// re-holding their agents from the reaper) so sessions survive a restart.
func NewAffinity(reg *Registry, st AffinityStore, cfg *config.Config, hold, release func(string), log func(string, ...any)) *Affinity {
	if log == nil {
		log = func(string, ...any) {}
	}
	a := &Affinity{
		reg: reg, store: st, cfg: cfg,
		hold: hold, release: release, log: log, now: time.Now,
		refs:  map[string]AffinityRef{},
		live:  map[string]Session{},
		locks: map[string]*sync.Mutex{},
		owned: map[string]string{},
	}
	if st != nil {
		for _, r := range st.Affinities() {
			bk := bindingKey(r.Agent, r.Key)
			a.refs[bk] = r
			a.owned[r.SessionID] = bk
			if a.hold != nil {
				a.hold(r.SessionID)
			}
		}
	}
	return a
}

func bindingKey(agent, key string) string { return agent + "\x00" + key }

// Dispatch routes one agent request through session affinity. handled=false
// means affinity doesn't apply (no session: spec, a non-persistent runtime,
// shadow) — the caller dispatches fresh as today. handled=true means this
// call owned the dispatch: either a follow-up to the bound session
// (RunRef.Queued, same AgentID) or a fresh spawn via runner that is now
// bound to the key.
func (a *Affinity) Dispatch(ctx context.Context, runner Runner, req dispatch.Request) (dispatch.RunRef, bool, error) {
	spec := req.Profile.Session
	if a == nil || spec == nil || req.Action.Type != "agent" || req.Shadow {
		return dispatch.RunRef{}, false, nil
	}
	c, err := a.reg.Resolve(req.Profile.RuntimeName())
	if err != nil {
		return dispatch.RunRef{}, false, nil // the plain path surfaces resolution errors
	}
	if !SupportsSessionPersistence(c) {
		return dispatch.RunRef{}, false, nil // fresh-per-event fallback (lean on memory)
	}
	key, err := dispatch.RenderField(spec.Key, req)
	if err != nil {
		return dispatch.RunRef{}, true, fmt.Errorf("agent %q session.key: %w", req.Action.Agent, err)
	}
	if key = strings.TrimSpace(key); key == "" {
		return dispatch.RunRef{}, true, fmt.Errorf("agent %q session.key rendered empty for %s", req.Action.Agent, req.Trigger.Kind)
	}
	bk := bindingKey(req.Action.Agent, key)

	// Serialize per key: at most one prompt in flight; concurrent same-key
	// events queue here.
	lk := a.lockFor(bk)
	lk.Lock()
	defer lk.Unlock()

	if ref, ok := a.refFor(bk); ok {
		if reason := a.expiredReason(ref, spec); reason != "" {
			a.evictLocked(ctx, bk, ref, reason)
		} else {
			out, ferr := a.followup(ctx, bk, ref, req)
			if ferr == nil {
				ref.LastUsed = a.now()
				a.putRef(bk, ref)
				return out, true, nil
			}
			// A dead/unreachable session is stale state, not a step error:
			// drop the binding and fall through to a fresh spawn.
			a.log("affinity: %s/%s follow-up failed (%v) — starting fresh", ref.Agent, ref.Key, ferr)
			a.evictLocked(ctx, bk, ref, "follow-up failed")
		}
	}

	runRef, err := runner.Dispatch(ctx, req)
	if err != nil || runRef.AgentID == "" {
		return runRef, true, err
	}
	now := a.now()
	ref := AffinityRef{
		Agent: req.Action.Agent, Key: key,
		Controller: c.Name(), SessionID: runRef.AgentID,
		Created: now, LastUsed: now,
	}
	a.putRef(bk, ref)
	if a.hold != nil {
		a.hold(ref.SessionID) // idle between events must not mean reaped
	}
	a.log("affinity: %s bound to session %s (key %s)", ref.Agent, ref.SessionID, ref.Key)
	return runRef, true, nil
}

// followup delivers the request's rendered prompt to the bound session as a
// follow-up turn, resuming the session by id when this process doesn't hold
// it (restart). Blocks until the turn ends (drains the update stream), so
// the caller's key lock gives one-prompt-in-flight.
func (a *Affinity) followup(ctx context.Context, bk string, ref AffinityRef, req dispatch.Request) (dispatch.RunRef, error) {
	a.mu.Lock()
	sess := a.live[bk]
	a.mu.Unlock()
	if sess == nil {
		c, err := a.reg.ByName(ref.Controller)
		if err != nil {
			return dispatch.RunRef{}, err
		}
		if sess, err = c.ResumeSession(ctx, ref.SessionID, nil); err != nil {
			return dispatch.RunRef{}, err
		}
		a.mu.Lock()
		a.live[bk] = sess
		a.mu.Unlock()
	}
	prompt, err := dispatch.RenderPrompt(req)
	if err != nil {
		return dispatch.RunRef{}, err
	}
	ch, err := sess.Prompt(ctx, Message{Text: prompt})
	if err != nil {
		return dispatch.RunRef{}, err
	}
	out, turnErr := "", error(nil)
	for u := range ch {
		if u.Kind == UpdateDone {
			out, turnErr = u.Output, u.Err
		}
	}
	if turnErr != nil {
		return dispatch.RunRef{}, turnErr
	}
	a.log("affinity: %s follow-up delivered to session %s (key %s)", ref.Agent, ref.SessionID, ref.Key)
	return dispatch.RunRef{
		Backend: "session", Kind: req.Trigger.Kind,
		AgentID: ref.SessionID, Queued: true, Output: out,
	}, nil
}

// ObserveEvent applies end_on eviction: an event listed in a profile's
// session.end_on renders that profile's key from its own trigger context and
// evicts the matching binding, so `gh.pr_closed` for repo X PR N ends that
// PR's session. Called by the engine for every fired trigger, before gates.
func (a *Affinity) ObserveEvent(ctx context.Context, t core.Trigger) {
	if a == nil || a.cfg == nil {
		return
	}
	for name, p := range a.cfg.Agents {
		if p.Session == nil || !p.Session.EndsOn(t.Instance, t.Source, t.Kind) {
			continue
		}
		key, err := dispatch.RenderField(p.Session.Key, dispatch.Request{Trigger: t})
		if err != nil || strings.TrimSpace(key) == "" {
			continue
		}
		bk := bindingKey(name, strings.TrimSpace(key))
		lk := a.lockFor(bk)
		lk.Lock()
		if ref, ok := a.refFor(bk); ok {
			a.evictLocked(ctx, bk, ref, "end_on "+t.Instance+"."+t.Kind)
		}
		lk.Unlock()
	}
}

// EvictExpired reaps every binding past its idle_ttl or max_lifetime (or
// whose profile no longer declares a session). Called by the sweep loop.
func (a *Affinity) EvictExpired(ctx context.Context) int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	snapshot := make(map[string]AffinityRef, len(a.refs))
	for bk, r := range a.refs {
		snapshot[bk] = r
	}
	a.mu.Unlock()
	n := 0
	for bk, ref := range snapshot {
		spec := a.specFor(ref.Agent)
		reason := "profile no longer keeps sessions"
		if spec != nil {
			reason = a.expiredReason(ref, spec)
		}
		if reason == "" {
			continue
		}
		lk := a.lockFor(bk)
		lk.Lock()
		if cur, ok := a.refFor(bk); ok && cur.SessionID == ref.SessionID {
			a.evictLocked(ctx, bk, cur, reason)
			n++
		}
		lk.Unlock()
	}
	return n
}

// Run sweeps expired sessions until ctx ends.
func (a *Affinity) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.EvictExpired(ctx)
		}
	}
}

// SetClock overrides the clock (tests).
func (a *Affinity) SetClock(now func() time.Time) { a.now = now }

// Owns reports whether an agent/session id is bound to a key — the engine's
// archive paths skip owned agents so a shared session isn't archived after
// one step finishes.
func (a *Affinity) Owns(agentID string) bool {
	if a == nil || agentID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.owned[agentID]
	return ok
}

// ---------------------------------------------------------------------------

func (a *Affinity) lockFor(bk string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	lk, ok := a.locks[bk]
	if !ok {
		lk = &sync.Mutex{}
		a.locks[bk] = lk
	}
	return lk
}

func (a *Affinity) refFor(bk string) (AffinityRef, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.refs[bk]
	return r, ok
}

func (a *Affinity) putRef(bk string, ref AffinityRef) {
	a.mu.Lock()
	a.refs[bk] = ref
	a.owned[ref.SessionID] = bk
	a.mu.Unlock()
	if a.store != nil {
		if err := a.store.PutAffinity(ref); err != nil {
			a.log("affinity: persist %s/%s: %v", ref.Agent, ref.Key, err)
		}
	}
}

func (a *Affinity) specFor(agent string) *config.SessionSpec {
	if a.cfg == nil {
		return nil
	}
	return a.cfg.Agents[agent].Session
}

// expiredReason reports why a binding is expired ("" = still live).
func (a *Affinity) expiredReason(ref AffinityRef, spec *config.SessionSpec) string {
	now := a.now()
	if idle := now.Sub(ref.LastUsed); idle > spec.IdleTTLOrDefault() {
		return fmt.Sprintf("idle %s > idle_ttl %s", idle.Round(time.Minute), spec.IdleTTLOrDefault())
	}
	if age := now.Sub(ref.Created); age > spec.MaxLifetimeOrDefault() {
		return fmt.Sprintf("age %s > max_lifetime %s", age.Round(time.Minute), spec.MaxLifetimeOrDefault())
	}
	return ""
}

// evictLocked removes a binding (caller holds its key lock): close the live
// handle, release the reaper hold, archive the agent when its profile wants
// finished agents archived, and drop the persisted ref. The next same-key
// event starts a fresh session.
func (a *Affinity) evictLocked(ctx context.Context, bk string, ref AffinityRef, reason string) {
	a.mu.Lock()
	sess := a.live[bk]
	delete(a.live, bk)
	delete(a.refs, bk)
	delete(a.owned, ref.SessionID)
	a.mu.Unlock()
	if sess != nil {
		_ = sess.Close(ctx)
	}
	if a.release != nil {
		a.release(ref.SessionID)
	}
	if a.cfg != nil && a.cfg.Agents[ref.Agent].ArchiveWhenDone {
		if c, err := a.reg.ByName(ref.Controller); err == nil {
			if runner, rerr := c.Runner(); rerr == nil {
				_ = runner.Archive(ctx, ref.SessionID)
			}
		}
	}
	if a.store != nil {
		if err := a.store.DeleteAffinity(ref.Agent, ref.Key); err != nil {
			a.log("affinity: delete %s/%s: %v", ref.Agent, ref.Key, err)
		}
	}
	a.log("affinity: evicted %s session %s (key %s): %s", ref.Agent, ref.SessionID, ref.Key, reason)
}
