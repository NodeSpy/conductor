package controller

import (
	"context"
	"sync"
	"time"
)

// SessionRef is a persisted pointer to a live/resumable session bound to a PR.
// The broker keeps one live session per PR in memory; the ref is what survives a
// conductor restart, so the next follow-up re-attaches by id (ACP session/load;
// paseo re-binds the agent id) instead of spawning a duplicate agent.
type SessionRef struct {
	PRKey      string
	Controller string       // controller name that owns the session
	SessionID  string       // the controller's session/agent id
	Model      SessionModel // session_model at bind time
	UpdatedAt  time.Time
}

// SessionStore persists the broker's PR→session map so an interactive hand-off
// survives a restart. *store.Store satisfies it; tests inject an in-memory fake.
type SessionStore interface {
	PutSession(SessionRef) error
	DeleteSession(prKey string) error
	Sessions() []SessionRef
}

// Broker owns one live agent session per PR and funnels follow-ups through it, so
// every controller looks "live" to the engine regardless of its session model:
//
//   - native (paseo): the live session IS the launched agent; a follow-up is
//     `paseo send`, and "resume" re-binds the agent id.
//   - resumable (ACP): the session survives by id; the broker resumes it on
//     demand (session/load) and prompts it.
//   - oneshot: each turn is a fresh process held behind the same Session handle.
//
// This unifies the engine's "one worker per PR" rule at the abstraction level:
// interactive hand-off follow-ups and autonomous re-dispatch both reduce to
// Session.Prompt on the PR's bound session rather than a second agent. The PR→
// session map is persisted, so a hand-off parked for you is re-attachable after a
// conductor restart.
type Broker struct {
	reg   *Registry
	store SessionStore
	log   func(string, ...any)

	mu   sync.Mutex
	live map[string]Session    // prKey -> live session held by THIS process
	refs map[string]SessionRef // prKey -> persisted ref (survives restart)
}

// NewBroker builds a broker over a controller registry and an optional session
// store (nil = in-memory only, no restart survival). Persisted refs are loaded
// so a follow-up after a restart can resume the PR's session by id.
func NewBroker(reg *Registry, st SessionStore, log func(string, ...any)) *Broker {
	if log == nil {
		log = func(string, ...any) {}
	}
	b := &Broker{
		reg:   reg,
		store: st,
		log:   log,
		live:  map[string]Session{},
		refs:  map[string]SessionRef{},
	}
	if st != nil {
		for _, r := range st.Sessions() {
			b.refs[r.PRKey] = r
		}
	}
	return b
}

// Open opens a fresh session for prKey via the controller resolved for perAgent
// (explicit `controller:` → default:true → built-in paseo), runs its first turn,
// and binds it as the PR's live session (persisting the ref). h receives any
// permission/input requests the session raises.
func (b *Broker) Open(ctx context.Context, prKey, perAgent string, spec Spec, h Handler) (Session, error) {
	c, err := b.reg.Resolve(perAgent)
	if err != nil {
		return nil, err
	}
	sess, err := c.NewSession(ctx, spec, h)
	if err != nil {
		return nil, err
	}
	b.Bind(prKey, c, sess)
	return sess, nil
}

// Bind records an already-open session as the PR's live session and persists its
// ref. Used when a session was launched outside the broker (e.g. the engine's
// existing background dispatch) but should still be broker-owned so follow-ups
// funnel to it and it survives a restart.
func (b *Broker) Bind(prKey string, c Controller, sess Session) {
	ref := SessionRef{
		PRKey:      prKey,
		Controller: c.Name(),
		SessionID:  sess.ID(),
		Model:      c.Model(),
	}
	b.mu.Lock()
	b.live[prKey] = sess
	b.refs[prKey] = ref
	b.mu.Unlock()
	if b.store != nil {
		if err := b.store.PutSession(ref); err != nil {
			b.log("broker: persist session for %s: %v", prKey, err)
		}
	}
}

// Session returns a usable live session for prKey: the in-memory one if this
// process still holds it, else — when a ref was persisted (e.g. before a restart)
// — it re-attaches by id via the owning controller (ACP session/load; paseo
// re-binds the agent id). Returns (nil, nil) when no session is bound to the PR.
func (b *Broker) Session(ctx context.Context, prKey string, h Handler) (Session, error) {
	b.mu.Lock()
	if s := b.live[prKey]; s != nil {
		b.mu.Unlock()
		return s, nil
	}
	ref, ok := b.refs[prKey]
	b.mu.Unlock()
	if !ok {
		return nil, nil
	}
	c, err := b.reg.ByName(ref.Controller)
	if err != nil {
		return nil, err
	}
	sess, err := c.ResumeSession(ctx, ref.SessionID, h)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.live[prKey] = sess
	b.mu.Unlock()
	return sess, nil
}

// Followup delivers a follow-up turn to the PR's live session, resuming it by id
// if this process no longer holds it. It reports whether a session handled the
// turn — the engine uses this to keep "one worker per PR": a follow-up is routed
// to the existing session rather than spawning a duplicate. handled=false with a
// nil error means no session is bound to the PR (the caller dispatches fresh).
func (b *Broker) Followup(ctx context.Context, prKey, text string, h Handler) (bool, error) {
	sess, err := b.Session(ctx, prKey, h)
	if err != nil {
		return false, err
	}
	if sess == nil {
		return false, nil
	}
	ch, err := sess.Prompt(ctx, Message{Text: text})
	if err != nil {
		return true, err
	}
	for range ch { // drain the update stream (native controllers emit one terminal update)
	}
	return true, nil
}

// Close ends the PR's session (best-effort) and drops its live handle and
// persisted ref, so a completed/discarded hand-off isn't re-attached later.
func (b *Broker) Close(ctx context.Context, prKey string) {
	b.mu.Lock()
	sess := b.live[prKey]
	delete(b.live, prKey)
	delete(b.refs, prKey)
	b.mu.Unlock()
	if sess != nil {
		_ = sess.Close(ctx)
	}
	if b.store != nil {
		if err := b.store.DeleteSession(prKey); err != nil {
			b.log("broker: delete session for %s: %v", prKey, err)
		}
	}
}
