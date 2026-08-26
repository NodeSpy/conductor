// Package engine consumes Triggers from integrations and drives them through
// dedup, attempt caps, kill switch, shadow mode, dispatch, and notifications.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
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
	// Archive soft-deletes a finished agent immediately, so a non-interactive step's
	// agent (e.g. assess) doesn't linger in paseo until the reaper's next tick.
	Archive(ctx context.Context, agentID string) error
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
	RetryReady(key, kind, head string, soft int, base time.Duration, factor int, max time.Duration) (bool, time.Duration)
	Record(key, kind, sig, head string) error
	RecordAttempt(key, kind, head string) error
	LastCommentID(key string) int64
	AdvanceCommentID(key string, id int64) error
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
	hold       *dispatch.HoldSet // agent ids handed off to the user; the reaper never touches these
	pausePath  string            // control file; present = paused (toggled by pause/resume, no restart)
	ch         chan core.Trigger
	sem        chan struct{} // concurrent-agent cap; nil = unlimited

	budgetMu  sync.Mutex  // guards agentDisp (rolling agent-dispatch timestamps)
	agentDisp []time.Time // agent-dispatch times in the last hour (runaway budget)
}

// overAgentBudget prunes agent-dispatch timestamps older than an hour and reports
// whether the last hour already has max dispatches.
func (e *Engine) overAgentBudget(max int) bool {
	cut := time.Now().Add(-time.Hour)
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	kept := e.agentDisp[:0]
	for _, t := range e.agentDisp {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	e.agentDisp = kept
	return len(e.agentDisp) >= max
}

// recordAgentDispatch stamps an agent dispatch into the rolling budget window.
func (e *Engine) recordAgentDispatch() {
	e.budgetMu.Lock()
	e.agentDisp = append(e.agentDisp, time.Now())
	e.budgetMu.Unlock()
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
	// Hold is the shared "never reap" set for interactive hand-off agents; the
	// engine registers a background step's agent id here at launch and the reaper
	// skips it. nil disables the explicit hold (falls back to label/marker signals).
	Hold *dispatch.HoldSet
	// PausePath is a control file whose presence pauses dispatch (toggled by the
	// pause/resume commands without a restart). Empty disables the runtime pause.
	PausePath string
	Log       func(string, ...any)
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
		hold:      o.Hold,
		pausePath: o.PausePath,
		ch:        make(chan core.Trigger, 256),
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
		e.log("%s queue full, dropping", tag(t))
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

// agentGuidance returns the house tone/format guidance appended to a dispatched
// agent's prompt. Precedence, most specific first: the profile's own `guidance`,
// then the top-level `agent_guidance`, then the built-in concise/human default.
// At each level nil falls through, "" disables, and text (wrapped in the standard
// separator) is used.
func (e *Engine) agentGuidance(profile config.AgentProfile) string {
	switch {
	case profile.Guidance != nil:
		return wrapGuidance(*profile.Guidance)
	case e.cfg.AgentGuidance != nil:
		return wrapGuidance(*e.cfg.AgentGuidance)
	default:
		return dispatch.ConcisionGuidance
	}
}

// retryWhileDeferred re-runs a step while its output still signals "not ready"
// (matches rp.WhileOutputMatches) — e.g. critique deferring on pending CI — polling
// every rp.RetryInterval() up to rp.RetryTimeout(), then giving up (the sweep is the
// backstop). Returns the last RunRef. Only meaningful for a step that exited cleanly
// but reported it isn't done; a hard error is handled by the caller.
func (e *Engine) retryWhileDeferred(ctx context.Context, req dispatch.Request, ref dispatch.RunRef, rp *config.StepRetry) dispatch.RunRef {
	if rp == nil || rp.WhileOutputMatches == "" {
		return ref
	}
	re, err := regexp.Compile(rp.WhileOutputMatches)
	if err != nil {
		e.log("%s step retry: bad while_output_matches %q: %v — not retrying", tag(req.Trigger), rp.WhileOutputMatches, err)
		return ref
	}
	if !re.MatchString(ref.Output) {
		return ref // already ready
	}
	interval, timeout := rp.RetryInterval(), rp.RetryTimeout()
	deadline := time.Now().Add(timeout)
	e.log("%s step deferred (matches %q) — releasing its slot and retrying every %s for up to %s", tag(req.Trigger), rp.WhileOutputMatches, interval, timeout)
	for {
		if time.Now().After(deadline) {
			e.log("%s step still deferred after %s — giving up (sweep will retry)", tag(req.Trigger), timeout)
			return ref // still holding the slot
		}
		// Free the concurrency slot while we idle so a deferred step (just waiting on
		// CI) doesn't tie up capacity, then queue for one again before re-running. On
		// ctx cancel (shutdown) we return without re-acquiring — the workflow goroutine
		// is being torn down, so the slot accounting no longer matters.
		e.release()
		select {
		case <-ctx.Done():
			return ref
		case <-time.After(interval):
		}
		if !e.acquire(ctx) {
			return ref // cancelled while queued (shutdown)
		}
		r2, err := e.disp.Dispatch(ctx, req)
		if err != nil {
			return r2 // surface the error to the caller's normal handling (slot held)
		}
		ref = r2
		if !re.MatchString(ref.Output) {
			e.log("%s step retry cleared — ready", tag(req.Trigger))
			return ref // slot held
		}
	}
}

// wrapGuidance renders guidance text as its own prompt block, or "" if empty.
func wrapGuidance(t string) string {
	if t = strings.TrimSpace(t); t == "" {
		return ""
	}
	return "\n\n---\n" + t
}

// isPaused reports whether the runtime pause control file is present.
func (e *Engine) isPaused() bool {
	if e.pausePath == "" {
		return false
	}
	_, err := os.Stat(e.pausePath)
	return err == nil
}

// triggerHasLabel reports whether the object's labels (stamped into Context by the
// integration where available) include label, case-insensitively.
func triggerHasLabel(t core.Trigger, label string) bool {
	raw, ok := t.Context["labels"]
	if !ok {
		return false
	}
	var labels []string
	switch v := raw.(type) {
	case []string:
		labels = v
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				labels = append(labels, s)
			}
		}
	}
	for _, l := range labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

func (e *Engine) process(ctx context.Context, t core.Trigger) {
	key := t.Key()

	// Terminal state: drop dedup record, no dispatch.
	if t.Kind == core.KindClosed {
		_ = e.store.Delete(key)
		e.log("%s closed; dropped state", tag(t))
		return
	}

	// Kill switch (config) + runtime pause (a control file toggled by `pause`/
	// `resume` without a restart).
	if !e.cfg.Control.IsEnabled() {
		return
	}
	if e.isPaused() {
		e.log("%s skipped — conductor is paused", tag(t))
		return
	}
	// Per-PR/issue opt-out: a label on the object (e.g. `conductor:off`) parks it.
	if pl := e.cfg.Control.PauseLabel; pl != "" && triggerHasLabel(t, pl) {
		e.log("%s skipped — carries pause label %q", tag(t), pl)
		return
	}
	e.store.Touch(key)

	act, ok := t.Action.(config.Action)
	if !ok || !act.IsEnabled() {
		return
	}
	head := t.Target.HeadSHA

	// Comment high-water mark: a new_comment for a comment id at or below the mark
	// was already handled (webhook or a prior sweep) — skip it. This is what lets the
	// sweep re-list recent comments to recover missed ones without re-dispatching
	// old ones (the single-slot dedup can't distinguish them). The mark advances on
	// a successful new_comment dispatch below.
	if !t.Force && t.Kind == "new_comment" {
		if id := commentID(t); id > 0 && id <= e.store.LastCommentID(key) {
			return
		}
	}
	if t.Force {
		e.log("%s forced — bypassing dedup/liveness/backoff gates", tag(t))
	}

	// Dedup/attempt state is keyed per action variant so two variants of a kind on
	// the same PR/head don't collide. An unnamed (single) action keeps the bare
	// `kind` key, so existing state.json is honored with no migration.
	dkind := t.Kind
	if t.Variant != "" {
		dkind = t.Kind + "#" + t.Variant
	}

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
	if t.Force {
		// Forced: skip the dedup / liveness gates entirely and dispatch below.
	} else if liveGate {
		// A review workflow shouldn't re-run while its agent is parked for you.
		// Single-action fixers instead fall through to dispatch, which queues new
		// work to the agent already on this PR (or spawns one) — see paseo.go.
		if len(act.Steps) > 0 && e.disp.HasLiveAgent(ctx, key, t.Kind) {
			// Exception: a review re-request on a NEW head. If we've never dispatched
			// review_requested at the current head, the parked agent is reviewing stale
			// code — re-engage on the new head instead of being blocked indefinitely by
			// it. A same-head re-request (or a duplicate webhook delivery) has a recorded
			// attempt at this head, so it's suppressed here: no double-fire, no re-review
			// of identical code.
			if t.Kind == "review_requested" && head != "" && e.store.Attempts(key, dkind, head) == 0 {
				e.log("%s re-engaging — review re-requested on a new head %s (agent parked on older code)", tag(t), short(head))
			} else {
				e.log("%s skipped — an agent is already working/parked for it", tag(t))
				return
			}
		}
		// A live-gated kind never records "done" on dispatch — the sweep re-derives
		// reality (still dirty? threads unresolved? review pending?) each run, so
		// culled/failed work isn't abandoned. The backoff below bounds the retries.
	} else if t.Dedup != "" && e.store.LastSignature(key, dkind) == t.Dedup {
		return
	}

	// Past the soft threshold (max_attempts_per_head), gate retries behind a GROWING
	// backoff instead of a hard cap — a struggling (pr,kind,head) keeps getting
	// periodic retries with widening gaps (10m→30m→…→24h) rather than being abandoned
	// forever. Escalate once, when it first crosses the threshold.
	soft := act.MaxAttemptsPerHead
	if soft == 0 && t.Kind != "new_comment" {
		soft = defaultMaxAttempts
	}
	if soft > 0 && !t.Force {
		if n := e.store.Attempts(key, dkind, head); n >= soft {
			if ready, wait := e.store.RetryReady(key, dkind, head, soft, retryBackoffBase, retryBackoffFactor, retryBackoffMax); !ready {
				e.log("%s in backoff — %d attempts at %s, next retry in ~%s",
					tag(t), n, short(head), wait.Round(time.Minute))
				return
			}
			if n == soft { // first time past the threshold and now eligible — say so, once
				// notif.Emit audits the escalate for status/report (no separate row here).
				e.notif.Emit(ctx, notify.EventEscalate, t,
					fmt.Sprintf("still failing after %d tries at %s — backing off, will keep retrying periodically", soft, short(head)))
			}
		}
	}

	// Resolve profile, tokens, shadow.
	var profile config.AgentProfile
	if act.Type == "agent" {
		profile = e.cfg.Agents[act.Agent]
		if act.Prompt != "" {
			act.Prompt += dispatch.WriteWrapperGuidance
			act.Prompt += e.agentGuidance(profile)
			if act.RerequestReview {
				act.Prompt += dispatch.RerequestReviewGuidance
			}
			// NOTE: no HoldGuidance here. A top-level single-action agent is an
			// autonomous fixer (new_comment/changes_requested/merge_conflict/
			// failing_checks/issue_matched) — it should make the best decision and
			// finish, not pose courtesy questions. Interactive "ask me" behavior is
			// reserved for review hand-offs (steps.go → HandoffGuidance on background
			// steps). A fixer that can't proceed just stops; live-gated kinds re-derive
			// via the sweep.
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
		if !shadow {
			if liveGate {
				// Live-gated kinds don't consume the permanent dedup (the sweep re-derives
				// them), but we DO mark the head we dispatched at, so a same-head re-request
				// or duplicate delivery is suppressed while a genuinely new head re-engages
				// (see the live-gate exception above).
				_ = e.store.RecordAttempt(key, dkind, head)
			} else { // shadow previews never record
				_ = e.store.Record(key, dkind, t.Dedup, head)
			}
		}
		e.notif.Emit(ctx, notify.EventDispatch, t, "workflow")
		e.log("%s workflow (%d steps%s)", tag(t), len(act.Steps), shadowNote(shadow))
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
		// Runaway guard: cap agent dispatches per rolling hour. Over budget → shed
		// (record an attempt so live-gated/backoff kinds re-run once the window frees;
		// commands stay ungated). Protects the box from a webhook flood or a sweep
		// misfire spinning up unbounded agents.
		if max := e.cfg.Control.AgentsPerHour(); max > 0 && e.overAgentBudget(max) {
			e.log("%s agent budget reached (%d/hr) — shedding, will retry later", tag(t), max)
			_ = e.store.RecordAttempt(key, dkind, head) // so it isn't silently forgotten
			return
		}
		if !e.acquire(ctx) {
			return
		}
		e.recordAgentDispatch()
	}

	e.notif.Emit(ctx, notify.EventDispatch, t, act.Type)
	if act.Type == "command" {
		e.log("%s running (%s)", tag(t), actionDesc(act))
	}
	start := time.Now()
	ref, err := e.disp.Dispatch(ctx, req)
	took := time.Since(start).Round(time.Second)
	if act.Type == "command" && err == nil && !ref.Skipped {
		tail := ""
		if tl := tailOutput(ref.Output); tl != "" {
			tail = "\n" + tl
		}
		e.log("%s command done (%s) in %s%s", tag(t), ref.Backend, took, tail)
	}
	e.auditDispatch(t, ref, err)
	gated := act.Type == "agent" && !shadow

	// A catch-up whose PR already has a working agent did nothing — don't record it
	// (it isn't an attempt) and free the slot.
	if ref.Skipped {
		e.log("%s %s", tag(t), ref.Output)
		if gated {
			e.release()
		}
		return
	}
	// A dispatch killed by our own shutdown (ctx cancelled / `paseo run` SIGTERM'd)
	// isn't a real attempt — don't count it toward backoff or escalate; the sweep
	// re-derives it cleanly on restart.
	if err != nil && interruptedByShutdown(ctx, err) {
		e.log("%s interrupted by shutdown — not counting an attempt", tag(t))
		if gated {
			e.release()
		}
		return
	}
	if !shadow {
		switch {
		case liveGate:
			// Never mark "done" on dispatch — the sweep re-derives completion. Just
			// count the attempt; backoff bounds retries of an unfixable state.
			_ = e.store.RecordAttempt(key, dkind, head)
		case err != nil:
			// A failed dispatch: count the try but don't consume the dedup signature,
			// so it retries next time instead of being suppressed forever.
			_ = e.store.RecordAttempt(key, dkind, head)
		default:
			_ = e.store.Record(key, dkind, t.Dedup, head)
		}
	}

	if err != nil {
		if tl := tailOutput(ref.Output); tl != "" {
			e.log("%s command output (tail):\n%s", tag(t), tl)
		}
		e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("dispatch failed: %v", err))
		if gated {
			e.release()
		}
		return
	}
	// A comment was handled (fresh agent, queued, or adopted) — raise the high-water
	// mark so the sweep's re-listing of recent comments won't re-dispatch this one.
	if t.Kind == "new_comment" {
		if id := commentID(t); id > 0 {
			_ = e.store.AdvanceCommentID(key, id)
		}
	}

	if ref.Queued {
		// Work was handed to an agent already on the PR — no new agent, no slot to
		// hold; it'll drain the queue on its own.
		if ref.Adopted {
			e.log("%s adopted your open workspace agent %s", tag(t), ref.AgentID)
		} else {
			e.log("%s queued to agent %s", tag(t), ref.AgentID)
		}
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
		e.log("%s resuming workflow from step %d", tag(t), r.StepIndex)
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

// Backoff schedule for retries past max_attempts_per_head: 10m, 30m, 90m, … ×3
// each step, capped at 24h. Never a permanent give-up — a struggling PR just
// retries with widening gaps (longer elapsed ⇒ less likely recoverable).
const (
	retryBackoffBase   = 10 * time.Minute
	retryBackoffFactor = 3
	retryBackoffMax    = 24 * time.Hour
	// defaultMaxAttempts is the soft threshold (retries before backoff begins) when
	// an action doesn't set max_attempts_per_head. new_comment is exempt — distinct
	// comments share a kind@head attempt key, so a cap there would throttle real work.
	defaultMaxAttempts = 3
)

// tag is a stable log prefix tying a line to its integration + target + kind, so
// all work for one PR/issue is greppable: engine[<instance> <repo>#<num> <kind>#<variant>].
func tag(t core.Trigger) string {
	id := t.Target.Repo
	if t.Target.PR > 0 {
		id = fmt.Sprintf("%s#%d", id, t.Target.PR)
	} else if t.Target.Issue > 0 {
		id = fmt.Sprintf("%s#%d", id, t.Target.Issue)
	}
	return fmt.Sprintf("engine[%s %s %s]", t.Instance, id, t.Kind+variantSuffix(t.Variant))
}

// variantSuffix renders "#name" for logs when a trigger is a named variant.
func variantSuffix(v string) string {
	if v == "" {
		return ""
	}
	return "#" + v
}

// interruptedByShutdown reports whether a dispatch error is just the daemon going
// down (ctx cancelled, or the `paseo run` child killed by our SIGTERM) rather than
// a real failure — so we don't burn an attempt or escalate on a clean shutdown.
func interruptedByShutdown(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "signal: terminated") ||
		strings.Contains(s, "signal: killed") ||
		strings.Contains(s, "context canceled")
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
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "failed"
	case ref.Skipped:
		outcome = "skipped"
	case ref.Adopted:
		outcome = "adopted"
	case ref.Queued:
		outcome = "queued"
	case ref.Shadowed:
		outcome = "shadow"
	}
	entry := map[string]any{
		"event": "dispatch", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "backend": ref.Backend, "argv": ref.Argv,
		"shadow": ref.Shadowed, "agent_id": ref.AgentID, "outcome": outcome,
	}
	if err != nil {
		entry["error"] = err.Error()
		e.log("%s dispatch failed: %v", tag(t), err)
	} else {
		e.log("%s dispatched (backend=%s shadow=%v)", tag(t), ref.Backend, ref.Shadowed)
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
		e.log("%s flaky rerun run %d: %v: %s", tag(t), runID, err, out)
	} else {
		e.log("%s flaky rerun triggered (run %d)", tag(t), runID)
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

// commentID reads a new_comment trigger's source comment id from Context (0 if
// absent — e.g. an older trigger without the field, which then can't be gated).
func commentID(t core.Trigger) int64 {
	if t.Context == nil {
		return 0
	}
	return toInt64(t.Context["comment_id"])
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
