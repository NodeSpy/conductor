// Package engine consumes Triggers from integrations and drives them through
// dedup, attempt caps, kill switch, shadow mode, dispatch, and notifications.
package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
)

// Dispatcher runs a resolved request against a backend. *dispatch.Dispatcher
// satisfies it; tests inject fakes.
type Dispatcher interface {
	Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error)
	// WaitForAgent blocks until a launched background agent goes idle (or the
	// timeout fires), so a concurrency slot frees only when its work is done.
	WaitForAgent(ctx context.Context, id string, timeout time.Duration)
}

// Notifier emits notifications. *notify.Notifier satisfies it.
type Notifier interface {
	Emit(context.Context, string, core.Trigger, string)
}

// Store is the persistence surface the engine needs. *store.Store satisfies it.
type Store interface {
	GC() (int, error)
	Touch(key string)
	Delete(key string) error
	LastSignature(key, kind string) string
	Attempts(key, kind, head string) int
	Record(key, kind, sig, head string) error
	Audit(entry map[string]any)
}

// Engine is the central work loop.
type Engine struct {
	cfg     *config.Config
	store   Store
	disp    Dispatcher
	notif   Notifier
	author  dispatch.Author
	userTok func() (string, error)
	rerun   func(context.Context, core.Trigger, int64)
	log     func(string, ...any)
	ch      chan core.Trigger
	sem     chan struct{} // concurrent-agent cap; nil = unlimited
}

// Options configure an Engine.
type Options struct {
	Config    *config.Config
	Store     Store
	Dispatch  Dispatcher
	Notifier  Notifier
	Author    dispatch.Author
	UserToken func() (string, error)
	// Rerun, if set, overrides the flaky-CI rerun step (tests inject a spy).
	Rerun func(context.Context, core.Trigger, int64)
	Log   func(string, ...any)
}

// New builds an Engine.
func New(o Options) *Engine {
	log := o.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	e := &Engine{
		cfg: o.Config, store: o.Store, disp: o.Dispatch, notif: o.Notifier,
		author: o.Author, userTok: o.UserToken, log: log,
		ch: make(chan core.Trigger, 256),
	}
	if cap := o.Config.Control.AgentCap(); cap > 0 {
		e.sem = make(chan struct{}, cap)
	}
	e.rerun = o.Rerun
	if e.rerun == nil {
		e.rerun = e.rerunFailed
	}
	return e
}

// Emit enqueues a trigger for processing (non-blocking; drops if the queue is
// saturated, logging so it's visible).
func (e *Engine) Emit(ctx context.Context, t core.Trigger) {
	select {
	case e.ch <- t:
	default:
		e.log("engine: queue full, dropping %s %s", t.Kind, t.Key())
	}
}

// Run processes triggers until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if _, err := e.store.GC(); err != nil {
		e.log("engine: initial GC: %v", err)
	}
	go e.gcLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-e.ch:
			e.process(ctx, t)
		}
	}
}

func (e *Engine) gcLoop(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := e.store.GC(); err == nil && n > 0 {
				e.log("engine: GC evicted %d records", n)
			}
		}
	}
}

func (e *Engine) process(ctx context.Context, t core.Trigger) {
	key := t.Key()

	// Terminal state: drop dedup record, no dispatch.
	if t.Kind == core.KindClosed {
		_ = e.store.Delete(key)
		e.log("engine: %s closed; dropped state", key)
		return
	}

	// Kill switch.
	if !e.cfg.Control.IsEnabled() {
		return
	}
	e.store.Touch(key)

	act, ok := t.Action.(config.Action)
	if !ok || !act.IsEnabled() {
		return
	}
	head := t.Target.HeadSHA

	// Flaky-CI: rerun failed checks once before spawning a fix agent.
	if t.Kind == "failing_checks" && act.FlakyRerun.Enabled {
		maxRerun := act.FlakyRerun.Max
		if maxRerun <= 0 {
			maxRerun = 1
		}
		if e.store.Attempts(key, "failing_checks_rerun", head) < maxRerun {
			if runID := toInt64(t.Context["run_id"]); runID > 0 {
				e.rerun(ctx, t, runID)
				_ = e.store.Record(key, "failing_checks_rerun", head, head)
				e.store.Audit(map[string]any{"event": "flaky_rerun", "repo": t.Target.Repo,
					"number": t.Target.Number, "run_id": runID})
				return // wait for the rerun; a fresh failure will re-trigger
			}
		}
	}

	// Dedup: already acted on this exact state.
	if t.Dedup != "" && e.store.LastSignature(key, t.Kind) == t.Dedup {
		return
	}

	// Attempt cap → escalate (notify) instead of looping.
	if cap := act.MaxAttemptsPerHead; cap > 0 && e.store.Attempts(key, t.Kind, head) >= cap {
		e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("attempt cap (%d) reached at %s", cap, short(head)))
		e.store.Audit(map[string]any{"event": "escalate", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "head": head})
		_ = e.store.Record(key, t.Kind, t.Dedup, head)
		return
	}

	// Resolve profile, tokens, shadow.
	var profile config.AgentProfile
	if act.Type == "agent" {
		profile = e.cfg.Agents[act.Agent]
		if act.Prompt != "" {
			act.Prompt += dispatch.WriteWrapperGuidance
		}
	}
	appTok, _ := t.Context["app_token"].(string)
	userTok := ""
	if e.userTok != nil {
		userTok, _ = e.userTok()
	}
	shadow := e.cfg.Control.Shadow || (act.Shadow != nil && *act.Shadow)

	// Multi-step workflow: record now (so it doesn't re-fire), take a slot as
	// backpressure, and run the steps in their own goroutine (releasing the slot
	// when the foreground steps finish) so the engine loop isn't blocked by them.
	if len(act.Steps) > 0 {
		if !shadow { // shadow is a true preview: don't consume dedup
			_ = e.store.Record(key, t.Kind, t.Dedup, head)
		}
		e.notif.Emit(ctx, notify.EventDispatch, t, "workflow")
		e.log("engine: workflow %s %s (%d steps%s)", t.Kind, key, len(act.Steps), shadowNote(shadow))
		if !shadow && !e.acquire(ctx) {
			return
		}
		go func() {
			if !shadow {
				defer e.release()
			}
			e.runSteps(ctx, t, act, appTok, userTok, shadow)
		}()
		return
	}

	req := dispatch.Request{
		Trigger: t, Action: act, Profile: profile,
		Tokens: dispatch.Tokens{App: appTok, User: userTok},
		Author: e.author, Shadow: shadow,
	}

	// Coding agents are heavy and contend on a shared repo. Acquire a slot first
	// (this blocks the loop as backpressure when the cap is full), then hold it in
	// the background until the launched agent goes idle — so the cap bounds the
	// number of *running* agents. Commands (gh merge/update-branch, critique) are
	// cheap and ungated. Checks/record/notify stay synchronous either way.
	if act.Type == "agent" && !shadow {
		if !e.acquire(ctx) {
			return
		}
	}

	e.notif.Emit(ctx, notify.EventDispatch, t, act.Type)
	ref, err := e.disp.Dispatch(ctx, req)
	e.auditDispatch(t, ref, err)
	if !shadow { // shadow is a true preview: don't consume dedup/attempts
		_ = e.store.Record(key, t.Kind, t.Dedup, head)
	}

	gated := act.Type == "agent" && !shadow
	if err != nil {
		e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("dispatch failed: %v", err))
		if gated {
			e.release()
		}
		return
	}
	e.notif.Emit(ctx, notify.EventComplete, t, ref.Backend)
	if gated && !ref.Shadowed && ref.AgentID != "" {
		go func() {
			e.disp.WaitForAgent(ctx, ref.AgentID, agentWaitTimeout(profile))
			e.release()
		}()
	} else if gated {
		e.release()
	}
}

// acquire takes a concurrency slot, blocking until one is free (backpressure).
// Returns false if the context is cancelled first. No-op (true) when uncapped.
func (e *Engine) acquire(ctx context.Context) bool {
	if e.sem == nil {
		return true
	}
	select {
	case e.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release returns a concurrency slot.
func (e *Engine) release() {
	if e.sem != nil {
		<-e.sem
	}
}

// auditDispatch writes the dispatch audit entry and logs the outcome.
func (e *Engine) auditDispatch(t core.Trigger, ref dispatch.RunRef, err error) {
	entry := map[string]any{
		"event": "dispatch", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "backend": ref.Backend, "argv": ref.Argv,
		"shadow": ref.Shadowed, "agent_id": ref.AgentID,
	}
	if err != nil {
		entry["error"] = err.Error()
		e.log("engine: dispatch %s %s: %v", t.Kind, t.Key(), err)
	} else {
		e.log("engine: dispatched %s %s (backend=%s shadow=%v)", t.Kind, t.Key(), ref.Backend, ref.Shadowed)
	}
	e.store.Audit(entry)
}

// agentWaitTimeout bounds how long a slot is held waiting for an agent to idle,
// so a stuck agent eventually frees its slot. Derived from the profile's
// wait_timeout (plus grace), else a one-hour backstop.
func agentWaitTimeout(p config.AgentProfile) time.Duration {
	if d := p.WaitTimeout.D(); d > 0 {
		return d + 5*time.Minute
	}
	return time.Hour
}

// rerunFailed re-runs the failed jobs of a workflow run, as you.
func (e *Engine) rerunFailed(ctx context.Context, t core.Trigger, runID int64) {
	tok := ""
	if e.userTok != nil {
		tok, _ = e.userTok()
	}
	c := exec.CommandContext(ctx, "gh", "run", "rerun", fmt.Sprintf("%d", runID),
		"--failed", "--repo", t.Target.Repo)
	c.Env = append(os.Environ(), "GH_TOKEN="+tok)
	if out, err := c.CombinedOutput(); err != nil {
		e.log("engine: flaky rerun %s run %d: %v: %s", t.Target.Repo, runID, err, out)
	} else {
		e.log("engine: flaky rerun triggered for %s run %d", t.Target.Repo, runID)
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func shadowNote(shadow bool) string {
	if shadow {
		return ", shadow"
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
