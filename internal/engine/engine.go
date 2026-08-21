// Package engine consumes Triggers from integrations and drives them through
// dedup, attempt caps, kill switch, shadow mode, dispatch, and notifications.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

// Dispatcher runs a resolved request against a backend. *dispatch.Dispatcher
// satisfies it; tests inject fakes.
type Dispatcher interface {
	Dispatch(context.Context, dispatch.Request) (dispatch.RunRef, error)
	// WaitForAgent blocks until a launched background agent goes idle (or the
	// timeout fires), so a concurrency slot frees only when its work is done.
	WaitForAgent(ctx context.Context, id string, timeout time.Duration)
	// HasLiveAgent reports whether any non-archived conductor agent is already
	// working or parked for this PR+kind (gates re-dispatch of live-gated kinds).
	HasLiveAgent(ctx context.Context, prKey, kind string) bool
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
	RecordAttempt(key, kind, head string) error
	Audit(entry map[string]any)
	// Workflow-run persistence, so multi-step workflows resume across restarts.
	PutRun(r store.WorkflowRun) error
	DeleteRun(id string) error
	PendingRuns() []store.WorkflowRun
}

// Engine is the central work loop.
type Engine struct {
	cfg        *config.Config
	store      Store
	disp       Dispatcher
	notif      Notifier
	author     dispatch.Author
	userTok    func() (string, error)
	readTok    func() (string, error) // read-token override (nil = use the per-trigger App token)
	rerun      func(context.Context, core.Trigger, int64)
	refreshTok func(core.Trigger) (string, error) // re-mint the App token on resume
	log        func(string, ...any)
	ch         chan core.Trigger
	sem        chan struct{} // concurrent-agent cap; nil = unlimited
}

// Options configure an Engine.
type Options struct {
	Config    *config.Config
	Store     Store
	Dispatch  Dispatcher
	Notifier  Notifier
	Author    dispatch.Author
	UserToken func() (string, error)
	// ReadToken, if set, overrides the token used for API reads (GH_TOKEN) instead
	// of the per-trigger App installation token — for identity.read_token != "app".
	ReadToken func() (string, error)
	// Rerun, if set, overrides the flaky-CI rerun step (tests inject a spy).
	Rerun func(context.Context, core.Trigger, int64)
	// RefreshAppToken re-mints the App installation token for a persisted trigger
	// on resume (the persisted one is expired). Given the trigger's instance +
	// installation_id. nil disables workflow resume.
	RefreshAppToken func(core.Trigger) (string, error)
	Log             func(string, ...any)
}

// New builds an Engine.
func New(o Options) *Engine {
	log := o.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	e := &Engine{
		cfg: o.Config, store: o.Store, disp: o.Dispatch, notif: o.Notifier,
		author: o.Author, userTok: o.UserToken, readTok: o.ReadToken, log: log,
		ch: make(chan core.Trigger, 256),
	}
	if cap := o.Config.Control.AgentCap(); cap > 0 {
		e.sem = make(chan struct{}, cap)
	}
	e.rerun = o.Rerun
	if e.rerun == nil {
		e.rerun = e.rerunFailed
	}
	e.refreshTok = o.RefreshAppToken
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

	// Gate. Most kinds dedup on the acted signature (act once per state). But a
	// review's "done" is external — you submitted, so you're no longer a requested
	// reviewer — not "we launched something once." For those, gate on whether a
	// conductor agent for this PR is already working/parked instead of a permanent
	// dedup flag, so a still-pending review keeps coming back until you do it.
	liveGate := livenessGated(t.Kind)
	if liveGate {
		// A review workflow shouldn't re-run while its agent is parked for you.
		// Single-action fixers instead fall through to dispatch, which queues new
		// work to the agent already on this PR (or spawns one) — see paseo.go.
		if len(act.Steps) > 0 && e.disp.HasLiveAgent(ctx, key, t.Kind) {
			e.log("engine: %s %s skipped — an agent is already working/parked for it", t.Kind, key)
			return
		}
		// A live-gated kind never records "done" on dispatch — the sweep re-derives
		// reality (still dirty? threads still unresolved? review still pending?) each
		// run, so culled/failed work isn't abandoned. The one exception: once it has
		// hit the attempt cap for this exact state, stay quiet until the state
		// changes, so an unfixable PR isn't re-escalated every sweep.
		if cap := act.MaxAttemptsPerHead; cap > 0 &&
			e.store.Attempts(key, t.Kind, head) >= cap &&
			e.store.LastSignature(key, t.Kind) == t.Dedup {
			return
		}
	} else if t.Dedup != "" && e.store.LastSignature(key, t.Kind) == t.Dedup {
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
			if act.RerequestReview {
				act.Prompt += dispatch.RerequestReviewGuidance
			}
			if profile.ArchiveWhenDone {
				act.Prompt += dispatch.HoldGuidance
			}
		}
	}
	appTok, _ := t.Context["app_token"].(string)
	if e.readTok != nil { // identity.read_token override → reads use it, not the App token
		if tok, err := e.readTok(); err == nil && tok != "" {
			appTok = tok
		}
	}
	userTok := ""
	if e.userTok != nil {
		userTok, _ = e.userTok()
	}
	shadow := e.cfg.Control.Shadow || (act.Shadow != nil && *act.Shadow)

	// Multi-step workflow: record now (so it doesn't re-fire), take a slot as
	// backpressure, and run the steps in their own goroutine (releasing the slot
	// when the foreground steps finish) so the engine loop isn't blocked by them.
	if len(act.Steps) > 0 {
		if !shadow && !liveGate { // shadow previews and live-gated kinds don't consume dedup
			_ = e.store.Record(key, t.Kind, t.Dedup, head)
		}
		e.notif.Emit(ctx, notify.EventDispatch, t, "workflow")
		e.log("engine: workflow %s %s (%d steps%s)", t.Kind, key, len(act.Steps), shadowNote(shadow))
		if !shadow && !e.acquire(ctx) {
			return
		}
		run := e.newRun(t, act, shadow)
		go func() {
			if !shadow {
				defer e.release()
			}
			e.runSteps(ctx, run, t, act, appTok, userTok, shadow)
		}()
		return
	}

	req := dispatch.Request{
		Trigger: t, Action: act, Profile: profile,
		Tokens: dispatch.Tokens{App: appTok, User: userTok},
		Author: e.author, Shadow: shadow, CatchUp: t.CatchUp,
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
	if act.Type == "command" {
		e.log("engine: %s %s running (%s)", t.Kind, key, actionDesc(act))
	}
	start := time.Now()
	ref, err := e.disp.Dispatch(ctx, req)
	took := time.Since(start).Round(time.Second)
	if act.Type == "command" && err == nil && !ref.Skipped {
		tail := ""
		if tl := tailOutput(ref.Output); tl != "" {
			tail = "\n" + tl
		}
		e.log("engine: %s %s command done (%s) in %s%s", t.Kind, key, ref.Backend, took, tail)
	}
	e.auditDispatch(t, ref, err)
	gated := act.Type == "agent" && !shadow

	// A catch-up whose PR already has a working agent did nothing — don't record it
	// (it isn't an attempt) and free the slot.
	if ref.Skipped {
		e.log("engine: %s %s — %s", t.Kind, key, ref.Output)
		if gated {
			e.release()
		}
		return
	}
	if !shadow {
		switch {
		case liveGate:
			// Never mark "done" on dispatch — the sweep re-derives completion. Just
			// count the attempt so the per-head cap can escalate an unfixable state.
			_ = e.store.RecordAttempt(key, t.Kind, head)
		case err != nil:
			// A failed dispatch: count the try but don't consume the dedup signature,
			// so it retries next time instead of being suppressed forever.
			_ = e.store.RecordAttempt(key, t.Kind, head)
		default:
			_ = e.store.Record(key, t.Kind, t.Dedup, head)
		}
	}

	if err != nil {
		if tl := tailOutput(ref.Output); tl != "" {
			e.log("engine: %s %s command output (tail):\n%s", t.Kind, key, tl)
		}
		e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("dispatch failed: %v", err))
		if gated {
			e.release()
		}
		return
	}
	if ref.Queued {
		// Work was handed to an agent already on the PR — no new agent, no slot to
		// hold; it'll drain the queue on its own.
		e.log("engine: %s %s queued to agent %s", t.Kind, key, ref.AgentID)
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

// newRun builds (and, unless shadow, persists) a WorkflowRun so a multi-step
// workflow can resume across a restart. The persisted trigger has its tokens
// stripped (they're re-minted on resume) and its Action detached (stored raw).
func (e *Engine) newRun(t core.Trigger, act config.Action, shadow bool) store.WorkflowRun {
	run := store.WorkflowRun{
		ID:       t.Kind + ":" + t.Key(),
		Source:   t.Source,
		Instance: t.Instance,
		Kind:     t.Kind,
		Repo:     t.Target.Repo,
		Number:   t.Target.Number,
		Outputs:  map[string]map[string]any{},
	}
	tp := t
	tp.Action = nil
	tp.Context = sanitizeContext(t.Context)
	run.Trigger, _ = json.Marshal(tp)
	run.Action, _ = json.Marshal(act)
	if shadow || e.store == nil {
		run.ID = "" // shadow previews aren't tracked/persisted
		return run
	}
	_ = e.store.PutRun(run)
	return run
}

// saveRun persists an updated run (no-op for untracked/shadow runs).
func (e *Engine) saveRun(run store.WorkflowRun) {
	if run.ID != "" {
		_ = e.store.PutRun(run)
	}
}

// finishRun removes a completed/failed run from persistence.
func (e *Engine) finishRun(run store.WorkflowRun) {
	if run.ID != "" {
		_ = e.store.DeleteRun(run.ID)
	}
}

// sanitizeContext copies a trigger context minus secrets (re-minted on resume).
func sanitizeContext(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if k == "app_token" || k == "gh_token" {
			continue
		}
		out[k] = v
	}
	return out
}

// ResumeWorkflows re-runs any workflow that was in-flight when the conductor last
// stopped. Prior steps' outputs are restored; the interrupted step re-runs
// (at-least-once). Tokens are re-minted. Disabled if RefreshAppToken is unset.
func (e *Engine) ResumeWorkflows(ctx context.Context) {
	if e.refreshTok == nil {
		return
	}
	for _, r := range e.store.PendingRuns() {
		var t core.Trigger
		var act config.Action
		if json.Unmarshal(r.Trigger, &t) != nil || json.Unmarshal(r.Action, &act) != nil {
			e.log("engine: resume %s: unreadable, dropping", r.ID)
			_ = e.store.DeleteRun(r.ID)
			continue
		}
		t.Action = act
		appTok, err := e.refreshTok(t)
		if err != nil {
			e.log("engine: resume %s: app token: %v (leaving for next start)", r.ID, err)
			continue
		}
		if e.readTok != nil { // identity.read_token override
			if tok, terr := e.readTok(); terr == nil && tok != "" {
				appTok = tok
			}
		}
		userTok := ""
		if e.userTok != nil {
			userTok, _ = e.userTok()
		}
		if t.Context == nil {
			t.Context = map[string]any{}
		}
		t.Context["app_token"] = appTok
		run := r
		if run.Outputs == nil {
			run.Outputs = map[string]map[string]any{}
		}
		e.log("engine: resuming workflow %s from step %d", r.ID, r.StepIndex)
		e.store.Audit(map[string]any{"event": "resume", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "step_index": r.StepIndex})
		if !e.acquire(ctx) {
			return
		}
		go func() {
			defer e.release()
			e.runSteps(ctx, run, t, act, appTok, userTok, false)
		}()
	}
}

// livenessGated reports whether a kind's completion is EXTERNAL state the sweep
// re-derives each run (review still pending? PR still dirty? threads still
// unresolved?) rather than a one-shot "we dispatched once" flag. For these we
// never record "done" on dispatch — a culled/failed/incomplete agent would
// otherwise mark the work done and it'd be abandoned. Instead we gate on whether
// an agent is already working/parked for it, and let the sweep retry until the
// underlying condition clears. new_comment stays dedup-gated (keyed per comment
// id — each distinct comment must be handled, not collapsed to "an agent ran").
func livenessGated(kind string) bool {
	switch kind {
	case "review_requested", "merge_conflict", "changes_requested":
		return true
	}
	return false
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
