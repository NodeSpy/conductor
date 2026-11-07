package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/internal/systemone"
)

// processFlow handles a trigger whose lowered action carries a FlowRef — the
// connectors-model path. It runs after process()'s generic gates (kill
// switch, pause, dedup, backoff), so those behave identically for both
// schemas; here we add the flow-side filters, the scoped policy gates, and
// group batching, then hand the run to the flow runner.
func (e *Engine) processFlow(ctx context.Context, t core.Trigger, act config.Action, key, dkind, head string) {
	act, spec, tidx, ok := e.resolveFlow(act)
	if !ok {
		e.log("%s stale flow ref %q — its trigger is no longer in the config; dropping", tag(t), act.FlowRef)
		return
	}
	t.Action = act
	if !spec.IsEnabled() {
		return
	}
	// A connector's declared option_hooks (plugin-contract.md §2.2): the
	// trigger's own options.<option> become extra start/done/fail hooks,
	// exactly as if the operator had written them under hooks: — the generic
	// successor to a connector's own dispatch-time/completion feedback. A
	// copy of spec is mutated (not e.flow's own compiled one, which every
	// other firing of this trigger shares) so this never leaks across runs.
	if extra := connector.OptionHooks(t, spec.Options); len(extra) > 0 {
		spec.Hooks = append(append([]config.Hook{}, spec.Hooks...), extra...)
	}
	// Flow-side filters (synthetic sources): a non-matching event is dropped
	// before any dedup state is consumed.
	if ok, err := e.flow.FilterMatch(t, spec); err != nil {
		e.log("%s filter error: %v", tag(t), err)
		return
	} else if !ok {
		return
	}

	// Scoped policy: trigger → connector → global, most specific wins. (No
	// policy-level enabled — the global kill switch is the runtime
	// `conductor pause`; connectors and triggers disable via their own
	// `enabled:` fields, checked above and at source build.)
	pol := e.policyFor(spec)
	if pol.PauseLabel != nil && *pol.PauseLabel != "" && triggerHasLabel(t, *pol.PauseLabel) {
		e.log("%s skipped — carries pause label %q", tag(t), *pol.PauseLabel)
		return
	}
	if ig := pol.Ignore; ig != nil && len(ig.Users) > 0 {
		if author := t.AuthorLogin(); author != "" {
			for _, u := range ig.Users {
				if strings.EqualFold(u, author) {
					e.log("%s skipped — author %q is ignored by policy", tag(t), author)
					return
				}
			}
		}
	}
	if q := pol.QuietHours; q != nil {
		if in, until := flow.InQuietWindow(q, time.Now()); in {
			if q.Hold == nil || *q.Hold {
				delay := time.Until(until)
				e.log("%s quiet hours — holding for %s", tag(t), delay.Round(time.Minute))
				tt := t
				time.AfterFunc(delay, func() { e.Emit(context.WithoutCancel(ctx), tt) })
			} else {
				e.log("%s quiet hours — dropped (hold: false)", tag(t))
			}
			return
		}
	}

	shadow := (pol.Shadow != nil && *pol.Shadow) || (act.Shadow != nil && *act.Shadow)

	// Consume dedup state now (mirrors the legacy multi-step branch): the
	// event is committed to a run. NOT for grouped triggers: their events
	// buffer in the in-memory Grouper, so recording here would make a
	// restart drop the batch while dedup suppressed redelivery — silent
	// loss. Grouped events record at flush time instead (see runBatch).
	grouped := spec.Group != nil
	if !shadow && !grouped && e.coalesceQueued(t) {
		// Committed to the waiting run: consume a dedup signature so a
		// redelivery stays suppressed, but don't count a live-gated attempt —
		// it hasn't run.
		if !t.LevelTriggered() && !t.Force {
			_ = e.store.Record(key, dkind, t.Dedup, head)
		}
		e.log("%s already waiting for an agent slot — coalesced (newest event kept)", tag(t))
		return
	}
	if !shadow && !grouped {
		if t.LevelTriggered() || t.Force {
			_ = e.store.RecordAttempt(key, dkind, head)
		} else {
			_ = e.store.Record(key, dkind, t.Dedup, head)
		}
	}
	// A comment was accepted for handling — raise the high-water mark
	// (grouped comments raise it at flush, with the same reasoning).
	if id, _, ok := t.Cursor(); ok && !grouped {
		_ = e.store.AdvanceCommentID(key, cursorMarkKey(t), id)
	}

	if grouped {
		gkey, err := flow.GroupKey(spec.Group.Key, t, e.flowBaseData(t))
		if err != nil {
			// A key that doesn't render (bad template, or one that renders
			// empty — a typo'd path under missingkey=zero) must NOT share a
			// bucket across events: fall back to per-event identity, so a
			// broken key degrades to "no batching" instead of one
			// cross-entity batch. Logged once per trigger, not per event.
			if _, warned := e.groupWarn.LoadOrStore(act.FlowRef, true); !warned {
				e.log("%s group key: %v — batching per-event", tag(t), err)
			}
			gkey = flow.EventIdentity(t)
		}
		full := act.FlowRef + "\x00" + gkey
		e.grouper.Add(full, t, spec.Group.Window.D(), spec.Group.MaxWait.D())
		return
	}
	e.notif.Emit(ctx, notify.EventDispatch, t, "workflow")
	e.startFlowRun(ctx, t, spec, tidx, nil, shadow)
}

// startFlowRun runs one flow (or batch) in its own goroutine once it holds a
// concurrency slot. The wait for the slot happens in that goroutine, never on
// the engine loop: blocking the loop stalled EVERY trigger — gates, parking,
// closed-PR cleanup, a fresh review request — behind whatever long fixer runs
// held the slots, with nothing logged. Waiting runs are served by slot
// priority (slotPriority), and a second trigger for a flow+target that is
// already waiting coalesces into it (see coalesceQueued).
func (e *Engine) startFlowRun(ctx context.Context, t core.Trigger, spec config.TriggerSpec, tidx int, batch *flow.Batch, shadow bool) {
	if shadow {
		run := e.newFlowRun(t, spec, shadow)
		go func() {
			defer e.recoverDispatch(ctx, t, run, "flow dispatch")
			e.flow.Run(ctx, run, t, spec, tidx, batch, shadow)
		}()
		return
	}
	qk := queuedFlowKey(t)
	queuedAt := time.Now()
	act, _ := t.Action.(config.Action)
	caps := e.runCapsOf(t, act.FlowRef, spec)
	e.queuedMu.Lock()
	if e.queued == nil {
		e.queued = map[string]core.Trigger{}
	}
	e.queued[qk] = t
	if len(caps) > 0 {
		// A run under a max_runs cap may queue for long: its record is kept
		// until it starts, so a restart re-queues it (ResumeWorkflows)
		// instead of losing it.
		if e.queuedRec == nil {
			e.queuedRec = map[string]bool{}
		}
		e.queuedRec[qk] = true
		e.putQueuedRun(t)
	}
	e.queuedMu.Unlock()
	go func() {
		ok := e.acquireRunCaps(ctx, t, caps)
		if ok {
			if e.sem != nil && e.sem.full() {
				e.log("%s waiting for an agent slot (all %d busy)", tag(t), e.cfg.AgentCap())
			}
			if ok = e.acquireFor(ctx, t.Interactive()); !ok {
				releaseRunCaps(caps)
			}
		}
		// The newest trigger that coalesced into this wait is the one that runs.
		e.queuedMu.Lock()
		if nt, found := e.queued[qk]; found {
			t = nt
			delete(e.queued, qk)
		}
		persisted := e.queuedRec[qk]
		delete(e.queuedRec, qk)
		e.queuedMu.Unlock()
		if !ok {
			// Shutdown while waiting: a run under a cap keeps its queued
			// record and is re-queued on restart; any other, the sweep
			// re-derives.
			return
		}
		defer e.release()
		defer releaseRunCaps(caps)
		if persisted {
			_ = e.store.DeleteRun(queuedRunID(t))
		}
		if t.BoundToTarget() && e.closedSince(t.Key(), queuedAt) {
			// The target closed while this run waited for a slot: the work
			// it was bound to is gone.
			e.log("%s dropped — its target closed while waiting for an agent slot", tag(t))
			return
		}
		run := e.newFlowRun(t, spec, false)
		defer e.recoverDispatch(ctx, t, run, "flow dispatch")
		e.runFlow(ctx, run, t, spec, tidx, batch)
	}()
}

// resolveFlow re-finds act's trigger by its stable identity (flow.Runner.Resolve)
// and rebinds act.FlowRef to where that trigger sits in the CURRENT config. A
// recorded action's ref names the trigger's position when it was recorded,
// which an unrelated config change moves; every key derived from the ref
// downstream (run caps, queued-run records, group buckets) must use the
// current one, or a resumed run lands in a different bucket than a fresh run
// of the same trigger.
func (e *Engine) resolveFlow(act config.Action) (config.Action, config.TriggerSpec, int, bool) {
	spec, tidx, ref, ok := e.flow.Resolve(act)
	if ok {
		act.FlowRef = ref
	}
	return act, spec, tidx, ok
}

// fireParkedHooks fires the trigger's workflow-level `at: fail` hooks for a
// (PR, kind, head) the engine just PARKED: it kept failing and won't be
// retried until new commits, so it never reaches a run whose own fail hooks
// would say so (flow.FireParkedHooks: hook.failure.kind "parked", run.reason
// flow.ParkedReason). Off the engine loop: hooks make API calls. Fired once,
// on the pass that parks — later passes over a parked tuple stay silent.
func (e *Engine) fireParkedHooks(ctx context.Context, t core.Trigger, act config.Action, head string, attempts int) {
	if act.FlowRef == "" || e.flow == nil {
		return
	}
	act, _, _, ok := e.resolveFlow(act)
	if !ok {
		return
	}
	go e.flow.FireParkedHooks(context.WithoutCancel(ctx), t, act.FlowRef, head, attempts)
}

// closedRetention bounds how long a PR's close is remembered for closedSince —
// far longer than any wait for an agent slot.
const closedRetention = 24 * time.Hour

// markClosed records that key's PR just closed (merged or not).
func (e *Engine) markClosed(key string) {
	now := time.Now()
	e.queuedMu.Lock()
	defer e.queuedMu.Unlock()
	if e.closedAt == nil {
		e.closedAt = map[string]time.Time{}
	}
	for k, at := range e.closedAt {
		if now.Sub(at) > closedRetention {
			delete(e.closedAt, k)
		}
	}
	e.closedAt[key] = now
}

// stopFixers kills every fixer still running on t's PR, across all runtimes —
// the PR merged or closed, so its work is moot and any push it makes from here
// lands on a dead branch (or, as happened, re-creates it and opens a follow-up
// PR). Each stopped run ends as "stopped", not failed.
func (e *Engine) stopFixers(ctx context.Context, t core.Trigger) {
	if e.controllers == nil {
		return
	}
	n := 0
	for _, r := range e.controllers.Runners() {
		if s, ok := r.(interface {
			StopTarget(context.Context, string) int
		}); ok {
			n += s.StopTarget(ctx, t.Key())
		}
	}
	if n > 0 {
		e.log("%s closed; stopped %d running fixer(s)", tag(t), n)
		e.store.Audit(map[string]any{"event": "fixers_stopped", "repo": t.Target.Repo,
			"number": t.Target.Number, "count": n})
	}
}

// closedSince reports whether key's PR closed at or after since.
func (e *Engine) closedSince(key string, since time.Time) bool {
	e.queuedMu.Lock()
	defer e.queuedMu.Unlock()
	at, ok := e.closedAt[key]
	return ok && !at.Before(since)
}

// queuedFlowKey identifies a flow run waiting for a slot: the flow + target.
func queuedFlowKey(t core.Trigger) string {
	act, _ := t.Action.(config.Action)
	return act.FlowRef + "\x00" + t.Key()
}

// coalesceQueued folds t into an identical flow+target run already waiting for
// a slot, keeping the newest event, and reports whether it did. Without it a
// sweep re-emitting a still-pending review_requested every pass would stack a
// duplicate run per pass behind the cap — and record an attempt for each, so a
// review could park before it ever ran.
func (e *Engine) coalesceQueued(t core.Trigger) bool {
	qk := queuedFlowKey(t)
	e.queuedMu.Lock()
	defer e.queuedMu.Unlock()
	if _, ok := e.queued[qk]; !ok {
		return false
	}
	e.queued[qk] = t
	if e.queuedRec[qk] {
		e.putQueuedRun(t)
	}
	return true
}

// queuedRunPrefix marks a persisted run that has not started: it waits for a
// max_runs slot. ResumeWorkflows re-queues it rather than resuming it.
const queuedRunPrefix = "queued:"

// queuedRunID is the record a queued run is kept under: one per flow and
// target, as queued runs coalesce (queuedFlowKey).
func queuedRunID(t core.Trigger) string {
	act, _ := t.Action.(config.Action)
	return queuedRunPrefix + act.FlowRef + ":" + t.Key()
}

// putQueuedRun persists (or, after a coalesce, updates) t's queued record.
func (e *Engine) putQueuedRun(t core.Trigger) {
	if e.store == nil {
		return
	}
	run := flowRunRecord(t)
	run.ID = queuedRunID(t)
	_ = e.store.PutRun(run)
}

// requeueFlowRun puts a run that was queued when the daemon stopped back in
// the queue.
func (e *Engine) requeueFlowRun(ctx context.Context, r store.WorkflowRun, t core.Trigger, act config.Action) {
	act, spec, tidx, ok := e.resolveFlow(act)
	if !ok {
		e.log("engine: re-queue %s: trigger no longer in config — dropping", r.ID)
		_ = e.store.DeleteRun(r.ID)
		return
	}
	t.Action = act
	if extra := connector.OptionHooks(t, spec.Options); len(extra) > 0 {
		spec.Hooks = append(append([]config.Hook{}, spec.Hooks...), extra...)
	}
	if r.ID != queuedRunID(t) {
		_ = e.store.DeleteRun(r.ID) // the trigger moved in the config: re-keyed below
	}
	e.log("%s re-queued after a restart", tag(t))
	e.startFlowRun(ctx, t, spec, tidx, nil, false)
}

// runBatch fires a grouped batch: the last event is the representative
// trigger (freshest context/tokens), the whole burst rides under {{.group.*}}.
// runFlow runs one (non-shadow) flow run, then re-delivers what the live gate
// held on its target while it ran (heldlevel.go). Every non-shadow flow run —
// fresh, batch, resumed, retried — goes through here, so none can end without
// it. Deferred: a run that panics still ended.
func (e *Engine) runFlow(ctx context.Context, run store.WorkflowRun, t core.Trigger, spec config.TriggerSpec, tidx int, batch *flow.Batch) {
	defer e.redeliverHeld(t.Key())
	e.flow.Run(ctx, run, t, spec, tidx, batch, false)
}

func (e *Engine) runBatch(fullKey string, events []core.Trigger) {
	if len(events) == 0 {
		return
	}
	t := events[len(events)-1]
	ref, gkey, _ := strings.Cut(fullKey, "\x00")
	// The bucket's ref is the trigger's position when the batch opened; a
	// reload while it buffered may have moved the trigger. Re-find it by the
	// events' own action.
	act, _ := t.Action.(config.Action)
	if act.FlowRef == "" {
		act.FlowRef = ref
	}
	act, spec, tidx, ok := e.resolveFlow(act)
	if !ok {
		e.log("%s stale flow ref %q at batch fire — its trigger is no longer in the config; dropping %d events", tag(t), ref, len(events))
		return
	}
	ref = act.FlowRef
	for i := range events {
		if a, isAct := events[i].Action.(config.Action); isAct {
			a.FlowRef = ref
			events[i].Action = a
		}
	}
	// Consume dedup state now — the flush-time half of what processFlow does
	// for ungrouped triggers at accept time. Until this point nothing was
	// recorded, so a restart that dropped the in-memory batch leaves the
	// events redeliverable instead of silently suppressed. Redelivery within
	// the window can buffer an event twice; duplicates drop here by signature.
	events = e.recordBatch(events)
	if len(events) == 0 {
		return
	}
	t = events[len(events)-1]
	// Same option_hooks lowering processFlow does for an ungrouped trigger
	// (see its comment): the batch's own spec is a fresh copy from SpecFor,
	// so it needs the same treatment here, keyed off the representative
	// (last) event's own options/facts.
	if extra := connector.OptionHooks(t, spec.Options); len(extra) > 0 {
		spec.Hooks = append(append([]config.Hook{}, spec.Hooks...), extra...)
	}

	// The grouper's fire callback carries no ctx of its own; tie the run to
	// the engine's shutdown so a SATURATED acquire() can be interrupted
	// instead of wedging the flush goroutine forever on a stopping daemon.
	ctx := e.baseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	e.notif.Emit(ctx, notify.EventDispatch, t, fmt.Sprintf("workflow (batch of %d)", len(events)))
	e.log("%s grouped batch firing (%d events, key %q)", tag(t), len(events), gkey)

	// Synchronous: the grouper's one-run-per-key guarantee depends on this
	// call not returning until the run finishes.
	caps := e.runCapsOf(t, ref, spec)
	if !e.acquireRunCaps(ctx, t, caps) {
		return
	}
	defer releaseRunCaps(caps)
	if !e.acquire(ctx) {
		return
	}
	defer e.release()
	run := e.newFlowRun(t, spec, false)
	e.runFlow(ctx, run, t, spec, tidx, &flow.Batch{Key: gkey, Events: events})
}

// recordBatch writes each grouped event's dedup/attempt/comment-mark state
// as its batch fires, dropping intra-batch duplicates (the same signature
// redelivered while buffered). Mirrors processFlow's accept-time recording.
func (e *Engine) recordBatch(events []core.Trigger) []core.Trigger {
	seen := map[string]bool{}
	kept := make([]core.Trigger, 0, len(events))
	for _, ev := range events {
		key, dkind, head := ev.Key(), dedupKindOf(ev), ev.Target.HeadSHA
		sig := key + "\x00" + dkind + "\x00" + ev.Dedup
		if ev.Dedup != "" && seen[sig] {
			continue
		}
		seen[sig] = true
		kept = append(kept, ev)
		if ev.LevelTriggered() || ev.Force {
			_ = e.store.RecordAttempt(key, dkind, head)
		} else {
			_ = e.store.Record(key, dkind, ev.Dedup, head)
		}
		if cid, _, ok := ev.Cursor(); ok {
			_ = e.store.AdvanceCommentID(key, cursorMarkKey(ev), cid)
		}
	}
	return kept
}

// dedupKindOf mirrors process()'s per-variant dedup kind.
func dedupKindOf(t core.Trigger) string {
	if t.Variant != "" {
		return t.Kind + "#" + t.Variant
	}
	return t.Kind
}

// policyFor merges the policy scopes that govern one trigger.
func (e *Engine) policyFor(spec config.TriggerSpec) config.Policy {
	var connPol *config.Policy
	if ref, ok := e.cfg.ConnectorsMap[spec.Connector()]; ok {
		connPol = ref.Policy
	}
	return config.MergePolicy(e.cfg.Policy, connPol, spec.Policy)
}

// retryPolicyFor resolves the policy that governs an action's retry/backoff
// gate: the fully scoped merge for a flow trigger, the global block for a
// legacy action (legacy integrations carry no policy of their own).
func (e *Engine) retryPolicyFor(act config.Action) config.Policy {
	if act.FlowRef != "" && e.flow != nil {
		if _, spec, _, ok := e.resolveFlow(act); ok {
			return e.policyFor(spec)
		}
	}
	return config.MergePolicy(e.cfg.Policy)
}

// flowBaseData mirrors the runner's base scope for group-key rendering.
func (e *Engine) flowBaseData(t core.Trigger) map[string]any {
	d := map[string]any{
		"repo": t.Target.Repo, "owner": t.Target.Owner, "name": t.Target.Name,
		"pr": t.Target.PR, "issue": t.Target.Issue, "number": t.Target.Number,
		"head": t.Target.HeadSHA, "base": t.Target.BaseRef, "url": t.Target.HTMLURL,
		"kind": t.Kind, "title": t.Title,
	}
	for k, v := range t.Context {
		if _, ok := d[k]; !ok {
			d[k] = v
		}
	}
	return d
}

// newFlowRun persists a resumable run for a flow trigger (tokens stripped,
// exactly like legacy workflow runs).
func (e *Engine) newFlowRun(t core.Trigger, spec config.TriggerSpec, shadow bool) store.WorkflowRun {
	run := flowRunRecord(t)
	if shadow || e.store == nil {
		run.ID = ""
		return run
	}
	_ = e.store.PutRun(run)
	return run
}

// flowRunRecord is the persisted record of a flow run of t, not yet started.
func flowRunRecord(t core.Trigger) store.WorkflowRun {
	run := store.WorkflowRun{
		ID:       "flow:" + t.Kind + ":" + t.Key(),
		Source:   t.Source,
		Instance: t.Instance,
		Kind:     t.Kind,
		Repo:     t.Target.Repo,
		Number:   t.Target.Number,
		Outputs:  map[string]map[string]any{},
	}
	tp := t
	act, _ := tp.Action.(config.Action)
	tp.Action = nil
	tp.Context = sanitizeContext(t)
	run.Trigger, _ = json.Marshal(tp)
	run.Action, _ = json.Marshal(act)
	return run
}

// resumeFlowRun resumes one persisted flow run (the FlowRef path of
// ResumeWorkflows): re-find the spec, re-mint tokens, continue after the last
// checkpointed step.
func (e *Engine) resumeFlowRun(ctx context.Context, r store.WorkflowRun, t core.Trigger, act config.Action) {
	act, spec, tidx, ok := e.resolveFlow(act)
	if !ok {
		e.log("engine: resume %s: trigger no longer in config — dropping", r.ID)
		_ = e.store.DeleteRun(r.ID)
		return
	}
	t.Action = act
	// Persist the rebound ref with the run's next checkpoint.
	if raw, err := json.Marshal(act); err == nil {
		r.Action = raw
	}
	if !checkpointFits(r, spec.Steps) {
		// The trigger's steps changed under the checkpoint (a pack upgrade
		// while the daemon was down): its step index and pinned outputs no
		// longer line up with the current definition, so continuing would
		// skip steps or feed one step another's outputs. Start over on the
		// current definition — a resume is at-least-once anyway.
		e.log("%s resume: the trigger's steps changed since this run checkpointed — restarting it from the beginning on the current definition", tag(t))
		e.store.Audit(map[string]any{"event": "resume_restarted", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "run": r.ID, "step_index": r.StepIndex,
			"reason": "definition changed"})
		r.StepIndex, r.Outputs, r.StartHooksFired = 0, map[string]map[string]any{}, false
	}
	if extra := connector.OptionHooks(t, spec.Options); len(extra) > 0 {
		spec.Hooks = append(append([]config.Hook{}, spec.Hooks...), extra...)
	}
	e.log("%s resuming flow from step %d", tag(t), r.StepIndex)
	e.store.Audit(map[string]any{"event": "resume", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "step_index": r.StepIndex})
	caps := e.runCapsOf(t, act.FlowRef, spec)
	// A run in flight when the daemon stopped still holds its serialization
	// key: take it now, before the runs that were waiting on it are
	// re-queued (ResumeWorkflows re-queues those last).
	var held []runCap
	if len(caps) > 0 && caps[0].serial != "" && e.tryAcquireCap(caps[0]) {
		held, caps = caps[:1], caps[1:]
	}
	go func() {
		// Wait for the slot here, not on the caller: resume runs at startup,
		// and more persisted runs than slots must not stall it.
		if !e.acquireRunCaps(ctx, t, caps) {
			releaseRunCaps(held)
			return
		}
		defer releaseRunCaps(held)
		defer releaseRunCaps(caps)
		if !e.acquireFor(ctx, t.Interactive()) {
			return
		}
		defer e.release()
		defer e.recoverDispatch(ctx, t, r, "flow resume")
		e.runFlow(ctx, r, t, spec, tidx, nil)
	}()
}

// askChannelFor resolves the hand-off channel a background step presents on:
// an ask-capable connector by name, else nil (runtime-native — the
// notify-to-open-paseo fallback).
func (e *Engine) askChannelFor(name string) handoff.Channel {
	if name != "" && e.connectors != nil {
		if in, ok := e.connectors.Get(name); ok && in.Impl != nil {
			if ac, isAsk := in.Impl.(connector.AskChanneler); isAsk {
				ch, err := ac.AskChannel(in.DefaultOptions)
				if err != nil {
					e.log("handoff connector %q: %v", name, err)
					return nil
				}
				return ch
			}
		}
	}
	return nil
}

// flowAgentServices builds the engine-owned services the flow runner needs
// for agent/command steps: runtime resolution, tokens, guidance, and the
// background review hand-off.
func (e *Engine) flowAgentServices() flow.AgentServices {
	return flow.AgentServices{
		Dispatch: func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
			runner := Dispatcher(e.disp)
			if req.Action.Type == "agent" {
				if e.deciders.DecisionOnly(req.Step.Runtime) {
					// A decision runtime answers decide: steps only; resolution
					// never picks one for an agent, so reaching here means the
					// step PINNED it. Refuse loudly rather than launch nothing.
					return dispatch.RunRef{}, dispatch.Unrecoverable(fmt.Errorf(
						"runtime %q is a decision runtime — it answers decide: steps, not agent steps", req.Step.Runtime))
				}
				r, err := e.runnerFor(req.Step)
				if err != nil {
					// Unknown/unrunnable controller — the flow runner escalates
					// rather than treating it as an ordinary step failure (#60).
					return dispatch.RunRef{}, dispatch.Unrecoverable(err)
				}
				runner = r
				if dispatch.UsesLaunchFields(req) {
					// detach:/repo:/images: are carried out by the paseo
					// dispatcher only. A runner that would silently ignore
					// them is refused: an ignored detach: would launch an
					// owned, credentialed agent instead of a forgotten one.
					if lf, ok := runner.(interface{ HonorsLaunchFields() bool }); !ok || !lf.HonorsLaunchFields() {
						return dispatch.RunRef{}, dispatch.Unrecoverable(fmt.Errorf(
							"runtime %q cannot carry detach:/repo:/images: — they need the builtin paseo runtime", req.Step.Runtime))
					}
				}
			}
			req.Author = e.author
			return e.dispatchAgent(ctx, runner, req)
		},
		// Flow steps and flow resume run in their own per-run goroutine, not
		// on the engine's single shared dispatch loop, so they use the
		// retrying mode: a rate_limited/not_ready mint is worth a bounded
		// wait here instead of failing the step outright (plugin-contract.md
		// §1.11, internal/engine/credentials.go credentialsForFlow).
		Credentials: e.credentialsForFlow,
		Guidance: func(identity string, p config.Step, pol config.Policy) string {
			return e.agentGuidance(p, pol) + e.outcomeGuidance(identity, p)
		},
		Memory:           e.memoryPrompt,
		ResolveModel:     e.resolveModel,
		DecideCandidates: e.decideCandidates,
		Decide: func(ctx context.Context, runtime, protocol string, req systemone.Request) (flow.DecideAnswer, error) {
			return e.decide(ctx, runtime, protocol, req)
		},
		// Revise is the supervise loop's round-trip (#36 §11): the failure
		// context goes to the authoring agent's bound session (§10) and the
		// captured reply carries the revised plan. No affinity, no session:
		// profile, or no binding → ok=false and the plan escalates instead.
		Revise: func(ctx context.Context, identity string, t core.Trigger, prompt string) (string, bool, error) {
			if e.affinity == nil {
				return "", false, nil
			}
			step, model, ok := e.stepByIdentity(ctx, identity)
			if !ok {
				return "", false, nil
			}
			return e.affinity.Followup(ctx, step, identity, model, t, prompt)
		},
		Background: func(ctx context.Context, t core.Trigger, stepID, identity string, p config.Step, ref dispatch.RunRef, handoffConn string, actions dispatch.HandoffActions) {
			e.hold.Add(ref.AgentID)
			ch := e.askChannelFor(handoffConn)
			if ch != nil && e.broker != nil && ref.AgentID != "" {
				e.startReviewHandoff(ctx, t, stepID, identity, p, ref, ch, actions)
				return
			}
			e.notif.Emit(ctx, notify.EventNeedsInput, t,
				fmt.Sprintf("interactive agent for %q is live (agent %s) — open it to review/refine", stepID, ref.AgentID))
			// No ask channel: the agent is yours to drive in paseo, but the
			// step's watch:/idle_timeout: still govern its lifetime.
			e.startNativeHandoff(ctx, t, stepID, identity, p, ref.AgentID, actions)
		},
		Archive: func(agentID string) {
			if e.affinityOwns(agentID) {
				return // a keyed session outlives the step that used it
			}
			go func() { _ = e.archiveAgent(context.Background(), agentID) }()
		},
		TakeDone: e.TakeEarlyDone,
		// The agents/hour cap, shared with the legacy steps: path so a flood
		// through either is counted against one window.
		CheckRate: func() error {
			max := e.cfg.AgentsPerHour()
			if max <= 0 {
				return nil
			}
			if e.overAgentBudget(max) {
				e.store.Audit(map[string]any{"event": "budget_shed",
					"scope": "agents_per_hour", "reason": "rate", "limit": max})
				return fmt.Errorf("agents_per_hour cap of %d reached in the last hour — shedding this dispatch", max)
			}
			e.recordAgentDispatch()
			return nil
		},
		// The spend-budget layer (#36 §14): caps checked before each agent
		// step dispatches, usage charged/audited after it returns. The shed
		// audit row is NOT written here — this seam carries no target/step/
		// agent context (just the runtime and the resolved scope/reason), so
		// the flow runner (which has that context) writes the one rich
		// `budget_shed` row itself (auditBudgetShed) when this returns an error.
		CheckBudget: func(runtimeName string, wf *config.BudgetPolicy, wfScope string, est cost.Usage) (*cost.Reservation, error) {
			res, berr := e.checkSpendBudget(runtimeName, wf, wfScope, est)
			if berr != nil {
				return nil, berr
			}
			return res, nil
		},
		CancelBudget: func(res *cost.Reservation) { e.meter.Cancel(res) },
		RecordUsage: func(t core.Trigger, identity, runtimeName, stepID, runID, wfScope, savedWF string, res *cost.Reservation, u cost.Usage) {
			e.recordUsage(t, identity, runtimeName, stepID, runID, wfScope, res, u)
			// The outcome loop's engagement (#36 §18): this STEP acted on
			// this target; a later terminal signal resolves it. The key is
			// the step identity (design §4), which is stable across runs.
			e.store.RecordEngagement(t.Key(), store.Engagement{
				Key: identity, Runtime: runtimeName, Workflow: wfScope, SavedWorkflow: savedWF,
				Kind: t.Kind, Run: runID, CostUSD: u.CostUSD, Tokens: u.TotalTokens,
			})
		},
		FollowUp: e.agentFollowUp,
	}
}

// sendCapturer is the optional Runner capability behind gate revisions: send
// a follow-up prompt to a live agent and capture the completed turn's output
// (the paseo dispatcher's `paseo send --json`).
type sendCapturer interface {
	SendCapture(ctx context.Context, id, prompt string) (string, error)
}

// agentFollowUp routes a gate-revise prompt (#36 §16) back to the SAME
// agent: a session-bound profile (§10) goes through affinity; a paseo agent
// takes a captured follow-up turn. ok=false when neither applies — the gate
// escalates instead of revising (an honest "this runtime can't revise").
func (e *Engine) agentFollowUp(ctx context.Context, agentID, identity string, t core.Trigger, prompt string) (string, bool, error) {
	profile, model, _ := e.stepByIdentity(ctx, identity)
	if e.affinity != nil && profile.Session != nil {
		return e.affinity.Followup(ctx, profile, identity, model, t, prompt)
	}
	if agentID == "" {
		return "", false, nil
	}
	runner, err := e.runnerFor(profile)
	if err != nil {
		return "", false, err
	}
	if sc, ok := runner.(sendCapturer); ok {
		out, serr := sc.SendCapture(ctx, agentID, prompt)
		if serr != nil {
			return "", false, serr
		}
		return out, true, nil
	}
	return "", false, nil
}
