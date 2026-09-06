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
	locks map[string]*keyLock    // bindingKey → serialization lock (ACTIVE keys only; refcounted)
	owned map[string]string      // sessionID → bindingKey (archive guards)
	// queue/draining implement per-key FIFO follow-up delivery: mutex
	// waiters aren't ordered, so two near-simultaneous same-key events
	// could otherwise deliver INVERTED — the old inline path preserved
	// arrival order and a session is a conversation. Entries exist only
	// while a key has pending work (bounded, like locks).
	queue    map[string][]func()
	draining map[string]bool
}

// enqueue appends a delivery job to bk's FIFO and ensures one drainer.
func (a *Affinity) enqueue(bk string, job func()) {
	a.mu.Lock()
	a.queue[bk] = append(a.queue[bk], job)
	if !a.draining[bk] {
		a.draining[bk] = true
		go a.drainQueue(bk)
	}
	a.mu.Unlock()
}

// drainQueue runs bk's jobs in arrival order, exiting (and cleaning up) when
// the queue empties.
func (a *Affinity) drainQueue(bk string) {
	for {
		a.mu.Lock()
		jobs := a.queue[bk]
		if len(jobs) == 0 {
			delete(a.queue, bk)
			delete(a.draining, bk)
			a.mu.Unlock()
			return
		}
		job := jobs[0]
		a.queue[bk] = jobs[1:]
		a.mu.Unlock()
		job()
	}
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
		refs:     map[string]AffinityRef{},
		live:     map[string]Session{},
		locks:    map[string]*keyLock{},
		owned:    map[string]string{},
		queue:    map[string][]func(){},
		draining: map[string]bool{},
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

	// Live-binding fast path, WITHOUT the key lock: the engine's single
	// process goroutine calls Dispatch inline, and the key lock is held for a
	// session's whole turn — waiting on it here head-of-line-blocks every
	// other trigger (and eventually overflows Emit's buffer). The follow-up
	// is rendered now (cheap) and DELIVERED asynchronously; the legacy hot
	// path never reads the turn's output. LastUsed advances at enqueue so an
	// expiry sweep can't reap a session with queued work.
	if ref, ok := a.refFor(bk); ok && a.expiredReason(ref, spec) == "" {
		prompt, perr := dispatch.RenderPrompt(req)
		if perr != nil {
			return dispatch.RunRef{}, true, perr
		}
		ref.LastUsed = a.now()
		a.putRef(bk, ref)
		dctx := context.WithoutCancel(ctx)
		a.enqueue(bk, func() { a.deliver(dctx, runner, req, bk, c.Name(), key, prompt) })
		return dispatch.RunRef{
			Backend: "session", Kind: req.Trigger.Kind,
			AgentID: ref.SessionID, Queued: true,
		}, true, nil
	}

	// Evict-and/or-spawn path: serialized on the key lock. An in-flight turn
	// can only be holding it if the binding just missed the fast path (an
	// expiry racing a queued delivery) — rare and bounded.
	kl := a.acquireKey(bk)
	defer a.releaseKey(bk, kl)

	if ref, ok := a.refFor(bk); ok {
		if reason := a.expiredReason(ref, spec); reason != "" {
			a.evictLocked(ctx, bk, ref, reason)
		} else {
			// Raced with another binder while waiting on the lock: the key is
			// live again — enqueue the follow-up (the delivery goroutine
			// queues behind this lock until we return).
			prompt, perr := dispatch.RenderPrompt(req)
			if perr != nil {
				return dispatch.RunRef{}, true, perr
			}
			ref.LastUsed = a.now()
			a.putRef(bk, ref)
			dctx := context.WithoutCancel(ctx)
			a.enqueue(bk, func() { a.deliver(dctx, runner, req, bk, c.Name(), key, prompt) })
			return dispatch.RunRef{
				Backend: "session", Kind: req.Trigger.Kind,
				AgentID: ref.SessionID, Queued: true,
			}, true, nil
		}
	}

	runRef, err := runner.Dispatch(ctx, req)
	if err != nil || runRef.AgentID == "" {
		return runRef, true, err
	}
	a.bind(bk, req.Action.Agent, key, c.Name(), runRef.AgentID)
	return runRef, true, nil
}

// bind records a fresh (agent, key) → session binding and holds the agent
// from the reaper.
func (a *Affinity) bind(bk, agent, key, controllerName, sessionID string) {
	now := a.now()
	ref := AffinityRef{
		Agent: agent, Key: key,
		Controller: controllerName, SessionID: sessionID,
		Created: now, LastUsed: now,
	}
	a.putRef(bk, ref)
	if a.hold != nil {
		a.hold(ref.SessionID) // idle between events must not mean reaped
	}
	a.log("affinity: %s bound to session %s (key %s)", ref.Agent, ref.SessionID, ref.Key)
}

// deliver runs on the key's FIFO drainer (arrival order preserved): it takes
// the key lock (one prompt in flight per key; excludes the supervise-loop
// Followup and teardown goroutines) and sends the follow-up. A binding that vanished while queued
// (end_on fired) drops the prompt — the session's lifecycle is over, and
// resurrecting a fresh session for, say, a closed PR would be worse. A
// binding whose session is DEAD (the send fails) is evicted and the event
// spawns a fresh session bound to the key, exactly like the old inline
// fallback — the event is never lost.
func (a *Affinity) deliver(ctx context.Context, runner Runner, req dispatch.Request, bk, controllerName, key, prompt string) {
	kl := a.acquireKey(bk)
	defer a.releaseKey(bk, kl)

	ref, ok := a.refFor(bk)
	if !ok {
		a.log("affinity: %s session for key %q ended before its queued follow-up delivered — dropping the prompt", req.Action.Agent, key)
		return
	}
	if _, err := a.followup(ctx, bk, ref, prompt, false); err == nil {
		ref.LastUsed = a.now()
		// Re-check under a.mu semantics: only touch the binding if it is
		// still ours (an end_on may have unbound mid-turn; putRef would
		// resurrect it).
		if cur, still := a.refFor(bk); still && cur.SessionID == ref.SessionID {
			cur.LastUsed = ref.LastUsed
			a.putRef(bk, cur)
		}
		return
	} else {
		// A dead/unreachable session is stale state, not a lost event: drop
		// the binding and spawn fresh, binding the key to the new session.
		a.log("affinity: %s/%s follow-up failed (%v) — starting fresh", ref.Agent, ref.Key, err)
		a.evictLocked(ctx, bk, ref, "follow-up failed")
	}
	runRef, err := runner.Dispatch(ctx, req)
	if err != nil || runRef.AgentID == "" {
		a.log("affinity: %s fresh dispatch after dead session failed: %v", req.Action.Agent, err)
		return
	}
	a.bind(bk, req.Action.Agent, key, controllerName, runRef.AgentID)
}

// followup delivers text to the bound session as a follow-up turn, resuming
// the session by id when this process doesn't hold it (restart). Blocks
// until the turn ends (drains the update stream), so the caller's key lock
// gives one-prompt-in-flight. capture asks for the turn's output (waits for
// the whole turn on native runtimes — use only off the hot dispatch path).
func (a *Affinity) followup(ctx context.Context, bk string, ref AffinityRef, text string, capture bool) (string, error) {
	a.mu.Lock()
	sess := a.live[bk]
	a.mu.Unlock()
	if sess == nil {
		c, err := a.reg.ByName(ref.Controller)
		if err != nil {
			return "", err
		}
		if sess, err = c.ResumeSession(ctx, ref.SessionID, nil); err != nil {
			return "", err
		}
		a.mu.Lock()
		a.live[bk] = sess
		a.mu.Unlock()
	}
	ch, err := sess.Prompt(ctx, Message{Text: text, Capture: capture})
	if err != nil {
		return "", err
	}
	out, turnErr := "", error(nil)
	for u := range ch {
		if u.Kind == UpdateDone {
			out, turnErr = u.Output, u.Err
		}
	}
	if turnErr != nil {
		return "", turnErr
	}
	a.log("affinity: %s follow-up delivered to session %s (key %s)", ref.Agent, ref.SessionID, ref.Key)
	return out, nil
}

// Followup delivers text to the agent's bound session for the trigger's
// rendered key, waiting for and returning the turn's output — the supervise
// loop's revise round-trip (#36 §11). ok=false when the agent keeps no
// sessions or none is bound for this key (the plan then escalates instead
// of revising). Serialized on the key's lock like any prompt.
func (a *Affinity) Followup(ctx context.Context, agentName string, profile config.AgentProfile, t core.Trigger, text string) (string, bool, error) {
	spec := profile.Session
	if a == nil || spec == nil {
		return "", false, nil
	}
	key, err := dispatch.RenderField(spec.Key, dispatch.Request{Trigger: t})
	if err != nil || strings.TrimSpace(key) == "" {
		return "", false, err
	}
	bk := bindingKey(agentName, strings.TrimSpace(key))
	kl := a.acquireKey(bk)
	defer a.releaseKey(bk, kl)
	ref, ok := a.refFor(bk)
	if !ok {
		return "", false, nil
	}
	out, err := a.followup(ctx, bk, ref, text, true)
	if err != nil {
		return "", true, err
	}
	ref.LastUsed = a.now()
	a.putRef(bk, ref)
	return out, true, nil
}

// ObserveEvent applies end_on eviction: an event listed in a profile's
// session.end_on renders that profile's key from its own trigger context and
// evicts the matching binding, so `gh.pr_closed` for repo X PR N ends that
// PR's session. Called by the engine for every fired trigger, before gates.
//
// The UNBIND happens synchronously (the very next event for this key starts
// fresh — engine-loop ordering is preserved), but the session TEARDOWN
// (close/release/archive) waits for the key lock in its own goroutine, so an
// in-flight follow-up turn finishes first and the engine goroutine never
// blocks behind one.
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
		ref, ok := a.refFor(bk)
		if !ok {
			continue
		}
		reason := "end_on " + t.Instance + "." + t.Kind
		sess := a.unbind(bk, ref)
		a.log("affinity: evicted %s session %s (key %s): %s", ref.Agent, ref.SessionID, ref.Key, reason)
		go func(bk string, ref AffinityRef, sess Session) {
			kl := a.acquireKey(bk)
			defer a.releaseKey(bk, kl)
			a.teardown(context.WithoutCancel(ctx), ref, sess)
		}(bk, ref, sess)
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
		kl := a.acquireKey(bk)
		if cur, ok := a.refFor(bk); ok && cur.SessionID == ref.SessionID {
			a.evictLocked(ctx, bk, cur, reason)
			n++
		}
		a.releaseKey(bk, kl)
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

// keyLock is one binding key's serialization lock, refcounted so the locks
// map only ever holds ACTIVE keys: without the refcount, one mutex per
// (agent, key) accumulated forever — every PR a session ever bound leaked an
// entry for the daemon's lifetime.
type keyLock struct {
	mu   sync.Mutex
	refs int // guarded by a.mu: holders + waiters; the entry deletes at 0
}

// acquireKey blocks until the key's lock is held. Pair with releaseKey.
func (a *Affinity) acquireKey(bk string) *keyLock {
	a.mu.Lock()
	kl := a.locks[bk]
	if kl == nil {
		kl = &keyLock{}
		a.locks[bk] = kl
	}
	kl.refs++
	a.mu.Unlock()
	kl.mu.Lock()
	return kl
}

// releaseKey unlocks and drops the map entry when this was the last user —
// waiters hold a ref, so an entry in use is never deleted (and thus two
// goroutines can never serialize on DIFFERENT mutexes for one key).
func (a *Affinity) releaseKey(bk string, kl *keyLock) {
	kl.mu.Unlock()
	a.mu.Lock()
	kl.refs--
	if kl.refs == 0 && a.locks[bk] == kl {
		delete(a.locks, bk)
	}
	a.mu.Unlock()
}

// LockedKeys reports the live key-lock entries (tests: bounded growth).
func (a *Affinity) LockedKeys() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.locks)
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

// evictLocked removes a binding (caller holds its key lock): unbind the maps
// and persisted ref, then tear the session down. The next same-key event
// starts a fresh session.
func (a *Affinity) evictLocked(ctx context.Context, bk string, ref AffinityRef, reason string) {
	sess := a.unbind(bk, ref)
	a.teardown(ctx, ref, sess)
	a.log("affinity: evicted %s session %s (key %s): %s", ref.Agent, ref.SessionID, ref.Key, reason)
}

// unbind removes the binding from the in-memory maps and the persisted
// store, returning the live session handle (nil if none). Safe without the
// key lock — the maps are a.mu-guarded — so the engine goroutine can unbind
// synchronously while an in-flight turn still holds the key lock.
func (a *Affinity) unbind(bk string, ref AffinityRef) Session {
	a.mu.Lock()
	sess := a.live[bk]
	delete(a.live, bk)
	delete(a.refs, bk)
	delete(a.owned, ref.SessionID)
	a.mu.Unlock()
	if a.store != nil {
		if err := a.store.DeleteAffinity(ref.Agent, ref.Key); err != nil {
			a.log("affinity: delete %s/%s: %v", ref.Agent, ref.Key, err)
		}
	}
	return sess
}

// teardown closes the live handle, releases the reaper hold, and archives
// the agent when its profile wants finished agents archived. Callers that
// might race an in-flight turn hold the key lock.
func (a *Affinity) teardown(ctx context.Context, ref AffinityRef, sess Session) {
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
}
