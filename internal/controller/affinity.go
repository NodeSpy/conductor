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

// Session affinity (#36 §10, re-keyed by docs/design/agents-removal.md §3).
// By default each dispatch gets a fresh agent; a `session:` block instead
// binds a live session to the rendered key, and every event resolving to that
// key reaches the same session as a follow-up prompt with full prior context.
// One agent per PR, shared by a comment, a check failure, and a review-change
// alike.
//
// The binding is (RUNTIME, MODEL, key-value). Both extra dimensions are
// STRUCTURAL rather than identities: a live agent is one model on one
// runtime, so you can resume neither a paseo session on codex nor an opus
// step into a haiku session. Because a pack assigns a fleet per step, its
// model assignments partition affinity for free — same fleet + same key is
// one agent, a different fleet is a different agent, with no policy needed.
//
// Two scopes share that binding shape: a `session:` on the RUNTIME is the
// overall pool (its key is used as written, so steps share it), and a
// `session:` on a STEP is its own pool (its key is namespaced to the step
// identity, so an identical key string is still a distinct session). A step
// with no session: joins the runtime's pool; with neither, dispatch is fresh.
//
// The registry is serialized per binding key (at most
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

// AffinityRef is one persisted (runtime, model, key) → session binding.
type AffinityRef struct {
	// Runtime is the `runtimes:` entry the session lives on — one half of
	// the structural partition.
	Runtime string
	// Model is the RESOLVED model the session runs (empty for a bare
	// launch) — the other half. A bare-launch pool and a pinned-model pool
	// are deliberately distinct.
	Model string
	// Key is the rendered session key value; for a step-scoped session it is
	// already namespaced to the step identity (see StepSessionKey).
	Key        string
	Controller string // runtime implementation that owns the session
	SessionID  string // the runtime's session/agent id
	// AgentAuthored: the original dispatch's provenance, replayed on resume
	// so the deny-by-default egress survives restarts (#36 iso-review H5).
	AgentAuthored bool
	Created       time.Time // spawn time (max_lifetime)
	LastUsed      time.Time // last prompt (idle_ttl)
}

// AffinityStore persists the bindings in conductor's own state. *store.Store
// satisfies it; tests inject an in-memory fake.
type AffinityStore interface {
	PutAffinity(AffinityRef) error
	DeleteAffinity(runtime, model, key string) error
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

// Affinity owns the global (runtime, model, key) → session registry.
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
			bk := bindingKey(r.Runtime, r.Model, r.Key)
			a.refs[bk] = r
			a.owned[r.SessionID] = bk
			if a.hold != nil {
				a.hold(r.SessionID)
			}
		}
	}
	return a
}

// bindingKey is the registry key: the three structural dimensions joined by
// a separator no rendered key can contain.
func bindingKey(runtime, model, key string) string {
	return runtime + "\x00" + model + "\x00" + key
}

// StepSessionKey namespaces a STEP-scoped session key to the step identity,
// so a step's pool is distinct from the runtime's overall pool and from every
// other step's — even when the human-written key string is identical
// (docs/design/agents-removal.md §3).
func StepSessionKey(stepIdentity, rendered string) string {
	if stepIdentity == "" {
		return rendered
	}
	return stepIdentity + string(keySep) + rendered
}

// keySep joins a step identity to its rendered key. A control byte, so it
// cannot occur in a hand-written identity; renderKey refuses a RENDERED
// key that contains one, since event data could otherwise choose which
// pool a session lands in.
const keySep = '\x1f'

// Dispatch routes one agent request through session affinity. handled=false
// means affinity doesn't apply (no session: spec, a non-persistent runtime,
// shadow) — the caller dispatches fresh as today. handled=true means this
// call owned the dispatch: either a follow-up to the bound session
// (RunRef.Queued, same AgentID) or a fresh spawn via runner that is now
// bound to the key.
func (a *Affinity) Dispatch(ctx context.Context, runner Runner, req dispatch.Request) (dispatch.RunRef, bool, error) {
	if a == nil || req.Action.Type != "agent" || req.Shadow {
		return dispatch.RunRef{}, false, nil
	}
	runtimeName := a.runtimeNameFor(req.Step.Runtime)
	spec, stepScoped := a.specFor(req.Step, runtimeName)
	if spec == nil {
		return dispatch.RunRef{}, false, nil
	}
	c, err := a.reg.Resolve(req.Step.Runtime)
	if err != nil {
		return dispatch.RunRef{}, false, nil // the plain path surfaces resolution errors
	}
	if !SupportsSessionPersistence(c) {
		return dispatch.RunRef{}, false, nil // fresh-per-event fallback (lean on memory)
	}
	key, err := a.renderKey(spec, req, stepScoped)
	if err != nil {
		return dispatch.RunRef{}, true, err
	}
	bk := bindingKey(runtimeName, req.Model, key)

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
	a.bind(bk, runtimeName, req.Model, key, c.Name(), runRef.AgentID, req.AgentAuthored)
	return runRef, true, nil
}

// runtimeNameFor resolves the `runtimes:` entry a dispatch lands on: the
// step's own pin, else the fleet default, else the built-in paseo.
func (a *Affinity) runtimeNameFor(pinned string) string {
	if pinned != "" {
		return pinned
	}
	if a.cfg != nil {
		if def := a.cfg.DefaultRuntimeName(); def != "" {
			return def
		}
	}
	return config.BuiltinPaseoRuntime
}

// specFor resolves which session policy governs a dispatch: the STEP's own
// (its own pool, key namespaced to the step identity), else the RUNTIME's
// (the overall pool, key as written), else none.
func (a *Affinity) specFor(step config.Step, runtimeName string) (spec *config.SessionSpec, stepScoped bool) {
	if step.Session != nil {
		return step.Session, true
	}
	if a.cfg != nil {
		if rt, ok := a.cfg.Runtimes[runtimeName]; ok && rt.Session != nil {
			return rt.Session, false
		}
	}
	return nil, false
}

// renderKey renders a session key against the dispatch, namespacing it to the
// step identity for a step-scoped session.
func (a *Affinity) renderKey(spec *config.SessionSpec, req dispatch.Request, stepScoped bool) (string, error) {
	key, err := dispatch.RenderField(spec.Key, req)
	if err != nil {
		return "", fmt.Errorf("step %q session.key: %w", req.Identity, err)
	}
	if key = strings.TrimSpace(key); key == "" {
		return "", fmt.Errorf("step %q session.key rendered empty for %s", req.Identity, req.Trigger.Kind)
	}
	// The step-scope namespace is joined with \x1f, and specForRef reads
	// the binding back by cutting on the FIRST one. A rendered key
	// carrying that byte — it can arrive from event data, so it is not
	// hypothetical — would make a runtime-pool key parse as step-scoped
	// and be judged against the wrong session: spec, or a step's key
	// parse with a truncated identity. Refuse it at the boundary rather
	// than let it decide which pool a session joins.
	if strings.ContainsRune(key, keySep) {
		return "", fmt.Errorf("step %q session.key rendered a value containing a control byte (U+001F), which is reserved as the scope separator — template a key from fields that cannot carry one", req.Identity)
	}
	if stepScoped {
		key = StepSessionKey(req.Identity, key)
	}
	return key, nil
}

// bind records a fresh (runtime, model, key) → session binding and holds the
// agent from the reaper.
func (a *Affinity) bind(bk, runtimeName, model, key, controllerName, sessionID string, agentAuthored bool) {
	now := a.now()
	ref := AffinityRef{
		Runtime: runtimeName, Model: model, Key: key,
		Controller: controllerName, SessionID: sessionID,
		AgentAuthored: agentAuthored,
		Created:       now, LastUsed: now,
	}
	a.putRef(bk, ref)
	if a.hold != nil {
		a.hold(ref.SessionID) // idle between events must not mean reaped
	}
	a.log("affinity: %s bound to session %s (key %s)", ref.label(), ref.SessionID, ref.Key)
}

// label renders the binding's structural partition for logs.
func (r AffinityRef) label() string {
	if r.Model == "" {
		return r.Runtime + "/(bare)"
	}
	return r.Runtime + "/" + r.Model
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
		a.log("affinity: session for key %q ended before its queued follow-up delivered — dropping the prompt", key)
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
		a.log("affinity: %s/%s follow-up failed (%v) — starting fresh", ref.label(), ref.Key, err)
		a.evictLocked(ctx, bk, ref, "follow-up failed")
	}
	runRef, err := runner.Dispatch(ctx, req)
	if err != nil || runRef.AgentID == "" {
		a.log("affinity: %s fresh dispatch after dead session failed: %v", req.Identity, err)
		return
	}
	a.bind(bk, ref.Runtime, ref.Model, key, controllerName, runRef.AgentID, req.AgentAuthored)
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
		if sess, err = c.ResumeSession(ctx, ref.SessionID, ref.AgentAuthored, nil); err != nil {
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
	a.log("affinity: %s follow-up delivered to session %s (key %s)", ref.label(), ref.SessionID, ref.Key)
	return out, nil
}

// Followup delivers text to the agent's bound session for the trigger's
// rendered key, waiting for and returning the turn's output — the supervise
// loop's revise round-trip (#36 §11). ok=false when the agent keeps no
// sessions or none is bound for this key (the plan then escalates instead
// of revising). Serialized on the key's lock like any prompt.
func (a *Affinity) Followup(ctx context.Context, step config.Step, identity string, model string, t core.Trigger, text string) (string, bool, error) {
	if a == nil {
		return "", false, nil
	}
	runtimeName := a.runtimeNameFor(step.Runtime)
	spec, stepScoped := a.specFor(step, runtimeName)
	if spec == nil {
		return "", false, nil
	}
	key, err := a.renderKey(spec, dispatch.Request{Trigger: t, Step: step, Identity: identity}, stepScoped)
	if err != nil {
		return "", false, nil
	}
	bk := bindingKey(runtimeName, model, key)
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
	// end_on is declared on a session: block, which now lives on a runtime
	// (the overall pool) or a step (its own). Both are walked: the binding a
	// rendered key resolves to is looked up across every live model, since
	// the model dimension is structural and an eviction rule names none.
	reason := "end_on " + t.Instance + "." + t.Kind
	evict := func(spec *config.SessionSpec, runtimeName, identity string, stepScoped bool, step config.Step) {
		if spec == nil || !spec.EndsOn(t.Instance, t.Source, t.Kind) {
			return
		}
		key, err := a.renderKey(spec, dispatch.Request{Trigger: t, Step: step, Identity: identity}, stepScoped)
		if err != nil {
			return
		}
		for _, bk := range a.bindingsFor(runtimeName, key) {
			ref, ok := a.refFor(bk)
			if !ok {
				continue
			}
			sess := a.unbind(bk, ref)
			a.log("affinity: evicted %s session %s (key %s): %s", ref.label(), ref.SessionID, ref.Key, reason)
			go func(bk string, ref AffinityRef, sess Session) {
				kl := a.acquireKey(bk)
				defer a.releaseKey(bk, kl)
				a.teardown(context.WithoutCancel(ctx), ref, sess)
			}(bk, ref, sess)
		}
	}
	for name, rt := range a.cfg.Runtimes {
		evict(rt.Session, name, "", false, config.Step{})
	}
	a.cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if s.Session == nil {
			return
		}
		rn := a.runtimeNameFor(s.Runtime)
		evict(s.Session, rn, s.Identity(scope, slot), true, *s)
	})
}

// bindingsFor lists the live binding keys for one runtime and rendered key,
// across every model partition — an eviction rule names no model, so it ends
// that key's session on whichever model it happens to be running.
func (a *Affinity) bindingsFor(runtimeName, key string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for bk, r := range a.refs {
		if r.Runtime == runtimeName && r.Key == key {
			out = append(out, bk)
		}
	}
	return out
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
		spec := a.specForRef(ref)
		reason := "no session: policy governs this binding any more"
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
			a.log("affinity: persist %s/%s: %v", ref.label(), ref.Key, err)
		}
	}
}

// specForRef finds the session policy still governing a persisted binding:
// the runtime's overall pool for a plain key, or the step whose identity
// namespaces a step-scoped key. Nil means nothing declares it any more and
// the binding is reaped.
func (a *Affinity) specForRef(ref AffinityRef) *config.SessionSpec {
	if a.cfg == nil {
		return nil
	}
	ident, _, stepScoped := strings.Cut(ref.Key, string(keySep))
	if !stepScoped {
		if rt, ok := a.cfg.Runtimes[ref.Runtime]; ok {
			return rt.Session
		}
		return nil
	}
	var found *config.SessionSpec
	a.cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if found != nil || s.Session == nil {
			return
		}
		if s.Identity(scope, slot) == ident {
			found = s.Session
		}
	})
	return found
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
	a.log("affinity: evicted %s session %s (key %s): %s", ref.label(), ref.SessionID, ref.Key, reason)
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
		if err := a.store.DeleteAffinity(ref.Runtime, ref.Model, ref.Key); err != nil {
			a.log("affinity: delete %s/%s: %v", ref.label(), ref.Key, err)
		}
	}
	return sess
}

// teardown closes the live handle, releases the reaper hold, and archives the
// agent when the step that owned the binding wants finished agents archived.
// Callers that might race an in-flight turn hold the key lock.
func (a *Affinity) teardown(ctx context.Context, ref AffinityRef, sess Session) {
	if sess != nil {
		_ = sess.Close(ctx)
	}
	if a.release != nil {
		a.release(ref.SessionID)
	}
	if a.archiveWanted(ref) {
		if c, err := a.reg.ByName(ref.Controller); err == nil {
			if runner, rerr := c.Runner(); rerr == nil {
				_ = runner.Archive(ctx, ref.SessionID)
			}
		}
	}
}

// archiveWanted reports whether the step owning a step-scoped binding asked
// for its agent to be archived when done. A runtime-pool binding is shared,
// so no single step's preference applies to it.
func (a *Affinity) archiveWanted(ref AffinityRef) bool {
	if a.cfg == nil {
		return false
	}
	ident, _, stepScoped := strings.Cut(ref.Key, string(keySep))
	if !stepScoped {
		return false
	}
	want := false
	a.cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if s.Identity(scope, slot) == ident && s.ArchiveWhenDone {
			want = true
		}
	})
	return want
}
