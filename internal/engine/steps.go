package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/cost"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/expr"
	"github.com/NodeSpy/conductor/internal/gitdiff"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/store"
)

// runSteps executes a multi-step workflow: each step may use a different
// agent/model, gate on an `if` condition over prior outputs, and (agent steps)
// emit structured output that later steps reference via
// {{ .steps.<id>.outputs.<key> }} and `if` conditions like
// `steps.<id>.outputs.<key> == true`. Steps run to completion in order; the
// whole workflow runs in its own goroutine so the engine loop isn't blocked.
func (e *Engine) runSteps(ctx context.Context, run store.WorkflowRun, t core.Trigger, act config.Action, appTok, userTok string, shadow bool) {
	data := e.stepBaseData(t)
	stepsOut := map[string]any{}
	// Restore completed steps' outputs (resume) so `if:`/templating see them.
	for id, out := range run.Outputs {
		stepsOut[id] = map[string]any{"outputs": out}
	}
	data["steps"] = stepsOut

	for i := run.StepIndex; i < len(act.Steps); i++ {
		step := act.Steps[i]
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("step%d", i+1)
		}

		if step.If != "" {
			ok, err := expr.Eval(step.If, data)
			if err != nil {
				e.log("%s step %s if-error: %v", tag(t), id, err)
				e.store.Audit(map[string]any{"event": "step_error", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "error": err.Error()})
				e.finishRun(run)
				return
			}
			if !ok {
				e.store.Audit(map[string]any{"event": "step_skipped", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "if": step.If})
				run.StepIndex = i + 1
				e.saveRun(run)
				continue
			}
		}

		s := step
		// A legacy Action step carries no behavior fields of its own, so
		// `agent:` is where it points at some: a STEP REFERENCE
		// (`<workflow>/<step-id>`) naming a step in the operator's own
		// workflows. Anything else is a plain attribution label, and the
		// dispatch runs on the policy baseline alone — deny-by-default, so
		// no Action can inherit memory or a grant nobody wrote down.
		profile, identity := e.actionProfile(s.Agent)
		if s.Backend != "" {
			profile.Runtime = s.Backend
		}
		model, modelProvider := "", ""
		if s.Type == "agent" {
			var rt string
			model, rt, modelProvider = e.resolveModel(ctx, profile)
			if rt != "" && profile.Runtime == "" {
				// The fleet's winning model lives on that runtime; the
				// step named none, so dispatch where the model actually is.
				profile.Runtime = rt
			}
			if s.Background {
				// A background step hands off a live agent for you to drive and
				// close yourself; it sits idle *because* it's waiting for you, so
				// the reaper must never archive it. Force this regardless of the
				// referenced step — a stale-on-disk or mistaken
				// archive_when_done: true must not reap an interactive hand-off
				// out from under you.
				profile.ArchiveWhenDone = false
			}
			// No prompt of its own → act on the event itself (connector-neutral
			// event object), synthesized before the guidance stack. This legacy
			// single-action path is never a grouped batch (grouping is a flow
			// feature), so no group rides along.
			if s.Prompt == "" {
				s.Prompt = dispatch.EventPrompt(t, nil)
			}
			if s.Prompt != "" {
				s.Prompt += dispatch.WriteWrapperGuidance
				s.Prompt += e.agentGuidance(profile, e.retryPolicyFor(act))
				s.Prompt += e.memoryPrompt(identity, profile, t, "")
				// Only the interactive hand-off (a background step) is told to ask. A
				// non-background step runs autonomously — especially a schema step like
				// `assess`, which MUST produce its structured output and must never pause
				// asking (that surfaces as "waiting for permission" and fails the schema).
				if s.Background {
					s.Prompt += dispatch.HandoffGuidance
				} else if len(s.OutputSchema) == 0 {
					// Schema steps get their done instruction from the verb
					// schema directive instead — never both (see DoneGuidance).
					s.Prompt += dispatch.DoneGuidance
				}
			}
		}
		req := dispatch.Request{
			Trigger: t, Action: s, Step: profile, Identity: identity, Model: model, Provider: modelProvider,
			Tokens: dispatch.Tokens{App: appTok, User: userTok},
			Author: e.author, Shadow: shadow, Wait: !s.Background, Interactive: s.Background, Data: data,
		}
		// Resolve which controller runs this agent step (explicit `controller:` →
		// default:true → built-in paseo). Command steps use the base dispatcher. An
		// unrunnable controller fails the workflow fast, like any other step error.
		// For today's configs this is the paseo dispatcher — no behavior change.
		runner := Dispatcher(e.disp)
		if s.Type == "agent" {
			r, rerr := e.runnerFor(profile)
			if rerr != nil {
				e.log("%s step %s no runnable controller: %v", tag(t), id, rerr)
				e.store.Audit(map[string]any{"event": "step_error", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "error": rerr.Error()})
				e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("workflow step %q: no runnable controller: %v", id, rerr))
				e.finishRun(run)
				return
			}
			runner = r
		}
		// Both budget layers, which this path skipped entirely: a legacy
		// `steps:` workflow could dispatch unbounded agents and unbounded
		// spend while the single-action path next door was capped. The
		// runaway guard exists to protect the box from a webhook flood;
		// a workflow is the EASIEST way to produce one.
		var spendRes *cost.Reservation
		if s.Type == "agent" && !shadow {
			if max := e.cfg.AgentsPerHour(); max > 0 && e.overAgentBudget(max) {
				e.log("%s step %s: agent budget reached (%d/hr) — shedding, will retry later", tag(t), id, max)
				e.store.Audit(map[string]any{"event": "step_shed", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "reason": "agents_per_hour"})
				e.finishRun(run)
				return
			}
			res, berr := e.checkSpendBudget(e.runtimeOf(profile), nil, "", cost.Estimate(model, s.Prompt, ""))
			if berr != nil {
				e.shedForBudget(ctx, t, berr, shadow)
				e.finishRun(run)
				return
			}
			spendRes = res
			e.recordAgentDispatch()
		}
		e.log("%s step %s running (%s)", tag(t), id, actionDesc(s))
		start := time.Now()
		ref, err := e.dispatchAgent(ctx, runner, req)
		// A step that exited cleanly but reports it isn't done yet (e.g. critique
		// deferring on pending CI) is retried per its `retry:` policy — the workflow
		// won't complete a not-ready step.
		if err == nil && !s.Background && s.Retry != nil {
			ref = e.retryWhileDeferred(ctx, req, ref, s.Retry)
		}
		took := time.Since(start).Round(time.Second)
		// Settle the reservation with what the step actually spent, so the
		// rolling window reflects reality rather than the estimate.
		if spendRes != nil {
			if err != nil {
				e.meter.Cancel(spendRes)
			} else {
				e.recordUsage(t, identity, e.runtimeOf(profile), id, run.ID, "", spendRes, cost.FromRun(model, s.Prompt, ref.Output))
			}
		}
		// A background step launches a live agent and returns immediately; there's
		// no captured output to fold into later steps.
		outputs := map[string]any{}
		if !s.Background {
			outputs = extractOutputs(ref)
			if s.Type == "agent" && err == nil && !shadow {
				// Same resolved identity the dispatch and outcomes use, so a
				// harvested memory's provenance names the step, not the label.
				e.harvestMemory(t, identity, run.ID, ref.Output, profile.Memory)
			}
		}
		stepsOut[id] = map[string]any{"outputs": outputs}

		entry := map[string]any{"event": "step", "repo": t.Target.Repo, "number": t.Target.Number,
			"kind": t.Kind, "step": id, "backend": ref.Backend, "shadow": ref.Shadowed}
		if err != nil {
			entry["error"] = e.redact(err.Error())
			e.store.Audit(entry)
			failMsg := ""
			if tail := tailOutput(ref.Output); tail != "" {
				failMsg = "\n" + e.redact(tail)
			}
			e.log("%s step %s failed after %s: %s%s", tag(t), id, took, e.redact(err.Error()), failMsg)
			e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("workflow step %q failed: %s", id, e.redact(err.Error())))
			e.finishRun(run)
			return // fail-fast
		}
		// Record the step's decision/outputs so a misroute is diagnosable after the
		// fact (the audit only logged that a step ran, never what it decided).
		if !s.Background && len(outputs) > 0 {
			entry["outputs"] = outputs
		}
		e.store.Audit(entry)
		// Checkpoint: this step is done — advance past it and record its outputs so
		// a restart resumes at the NEXT step (the interrupted one re-runs).
		run.StepIndex = i + 1
		if !s.Background {
			run.Outputs[id] = outputs
		}
		e.saveRun(run)
		if s.Background {
			// Handed off to a live agent — tell the reaper never to touch it (this is
			// the authoritative keep-signal; a hand-off carries no pending permission
			// or hold marker for the reaper to observe), then hand it to you.
			e.hold.Add(ref.AgentID)
			e.log("%s step %s launched in background after %s (agent %s)", tag(t), id, took, ref.AgentID)
			// Resolve the step's hand-off channel (explicit `handoff:` name → the
			// default:true entry → the sole configured entry). A step naming an
			// unknown handoff is caught by config validation before a live trigger
			// ever reaches here, but resolve defensively and escalate rather than
			// silently falling back if it somehow does.
			var handoffCh handoff.Channel
			if e.handoffs != nil {
				ch, herr := e.handoffs.Resolve(s.Handoff)
				if herr != nil {
					e.log("%s step %s handoff %q: %v", tag(t), id, s.Handoff, herr)
					e.notif.Emit(ctx, notify.EventEscalate, t,
						fmt.Sprintf("workflow step %q: handoff %q: %v", id, s.Handoff, herr))
				}
				handoffCh = ch
			}
			// With a hand-off channel resolved, rewire the review over the session
			// broker + channel (present → await → revise/submit), controller-agnostic.
			// Without one (none configured, or resolution came up empty), keep today's
			// behavior: tell you to drive the agent in paseo.
			if handoffCh != nil && e.broker != nil && ref.AgentID != "" {
				// The RESOLVED identity, not the raw `agent:` label: the
				// hand-off records its decision under OutcomeKeyFor(identity),
				// so passing s.Agent filed the outcome under a key nothing
				// else uses — the step's track record silently never
				// accumulated, and the memory/session machinery looked for it
				// under the identity this step actually has.
				// The legacy path can rerun_step (re-dispatch this step +
				// re-hand-off, self-referential so the replacement is itself
				// watchable); it has no workflow surface, so RunWorkflow is nil.
				var actions dispatch.HandoffActions
				actions = dispatch.HandoffActions{
					RerunStep: func(c context.Context, extraPrompt string) error {
						r2 := req
						if strings.TrimSpace(extraPrompt) != "" {
							r2.Action.Prompt = strings.TrimRight(r2.Action.Prompt, "\n") + "\n\n" + extraPrompt
						}
						nref, derr := e.dispatchAgent(c, runner, r2)
						if derr != nil {
							return derr
						}
						e.hold.Add(nref.AgentID)
						e.startReviewHandoff(c, t, id, identity, profile, nref, handoffCh, actions)
						return nil
					},
				}
				e.startReviewHandoff(ctx, t, id, identity, profile, ref, handoffCh, actions)
			} else {
				e.notif.Emit(ctx, notify.EventNeedsInput, t,
					fmt.Sprintf("interactive agent for %q is live in paseo (agent %s) — open it to review/refine", id, ref.AgentID))
			}
			continue
		}
		// Agent steps log their structured decision; command steps get a tail of their
		// stdout (the result is at the end; the go-build/download preamble is dropped).
		summary := ""
		if s.Type == "agent" {
			summary = logOutputs(outputs)
		} else if tail := tailOutput(ref.Output); tail != "" {
			summary = "\n" + tail
		}
		e.log("%s step %s done (%s) in %s%s", tag(t), id, ref.Backend, took, summary)

		// A non-interactive agent step (e.g. assess) needs no interaction, so archive
		// its agent the instant it finishes rather than leaving it to clutter paseo
		// until the reaper's next poll. Fire-and-forget; the reaper is the backstop.
		// A keyed session (affinity) is shared across events — never archived here.
		if s.Type == "agent" && profile.ArchiveWhenDone && ref.AgentID != "" && !e.affinityOwns(ref.AgentID) {
			go func(id string) { _ = e.archiveAgent(context.Background(), id) }(ref.AgentID)
		}
	}
	e.notif.Emit(ctx, notify.EventComplete, t, "workflow")
	e.finishRun(run)
}

// startReviewHandoff rewires an interactive review hand-off onto the session
// broker + the step's resolved hand-off channel: it binds the just-launched
// agent as the PR's broker session (so a follow-up funnels to it and the
// hand-off survives a restart), then runs the present → await → revise/submit
// loop on ch in the background — controller-agnostic, driving the live session
// via Prompt (for the paseo controller, `paseo send`). If the controller can't be
// resolved or the agent can't be bound, it falls back to today's behavior
// (notify you to open the agent in paseo). Only invoked when ch and the broker
// are configured.
func (e *Engine) startReviewHandoff(ctx context.Context, t core.Trigger, stepID, identity string, profile config.Step, ref dispatch.RunRef, ch handoff.Channel, actions dispatch.HandoffActions) {
	agentID := ref.AgentID
	fallback := func(reason string) {
		if reason != "" {
			e.log("%s review hand-off: %s — leaving agent %s live in paseo", tag(t), reason, agentID)
		}
		e.notif.Emit(ctx, notify.EventNeedsInput, t,
			fmt.Sprintf("interactive agent for %q is live in paseo (agent %s) — open it to review/refine", stepID, agentID))
	}
	c, err := e.controllerFor(profile)
	if err != nil {
		fallback(fmt.Sprintf("no controller: %v", err))
		return
	}
	prKey := t.Key()
	notifyRef := func(ref string) {
		e.notif.Emit(ctx, notify.EventNeedsInput, t,
			fmt.Sprintf("review for %q is ready — approve/revise/discard here: %s", stepID, ref))
	}
	handler := handoff.NewHandler(ch, notifyRef)
	// Legacy engine steps are config-authored; the flow runner owns
	// agent-authored plans — so this hand-off's provenance is never
	// agent-authored.
	sess, err := c.ResumeSession(ctx, agentID, false, handler)
	if err != nil {
		fallback(fmt.Sprintf("bind agent %s: %v", agentID, err))
		return
	}
	e.broker.Bind(prKey, c, sess, false)
	draft := handoff.Draft{
		Title:  fmt.Sprintf("Review for %s", prKey),
		Body:   "The agent is preparing its review. Edit the text and choose Send revision to hand it back to the agent, Approve to have it submit as-is, or Discard.",
		PRKey:  prKey,
		Repo:   t.Target.Repo,
		Number: t.Target.Number,
	}
	// Diff preview (#36 §17): every presentation of this hand-off carries the
	// agent's CURRENT proposed diff, read live from its worktree — the draft
	// is a real diff, not just prose. Remote/worktree-less runs present prose
	// only (there is no local path to read).
	var refresh func(*handoff.Draft)
	if wd := ref.Workdir; wd != "" {
		refresh = func(d *handoff.Draft) {
			diff, derr := gitdiff.Proposed(ctx, wd, 48<<10)
			if derr != nil || strings.TrimSpace(diff) == "" {
				return
			}
			d.Body += "\n\n--- proposed diff (live) ---\n" + e.redact(diff)
		}
	}
	// The review loop runs under a cancelable ctx so the hand-off can be ended out
	// of band: a watch rule (bail/refresh), the agent calling handoff.done, or the
	// idle timeout — each cancels → Review's Await returns → the loop ends. The
	// live-hand-off registry lets handoff.done and the idle timer find THIS one.
	runCtx, cancel := context.WithCancel(ctx)
	e.registerLiveHandoff(agentID, cancel, prKey, stepID)
	if profile.Watch != nil {
		e.startHandoffWatch(ctx, runCtx, cancel, t, stepID, identity, agentID, prKey, profile, ch, actions)
	}
	if d := time.Duration(profile.IdleTimeout); d > 0 {
		e.startIdleTimer(ctx, runCtx, t, stepID, agentID, d)
	}
	go func() {
		defer e.recoverDispatch(ctx, t, store.WorkflowRun{}, "review hand-off")
		// On ANY exit (resolved, bailed, refreshed, done, idle) stop the watch/
		// idle goroutines and drop the registry entry.
		defer cancel()
		defer e.deregisterLiveHandoff(agentID)
		dec, rerr := handoff.Review(runCtx, sess, ch, draft, notifyRef, refresh)
		if rerr != nil {
			if runCtx.Err() == nil {
				e.log("%s review hand-off loop for %q ended: %v", tag(t), stepID, rerr)
			}
			return
		}
		e.log("%s review hand-off for %q resolved: %s", tag(t), stepID, dec.Action)
		// The outcome loop (#36 §18): the human's terminal call on this
		// step's work is a quality signal, keyed by its track record.
		e.recordDecisionOutcome(t, OutcomeKeyFor(identity, profile), dec.Action)
		if dec.Action == handoff.ActionDiscard {
			e.broker.Close(ctx, prKey)
		}
	}()
}

// startHandoffWatch runs a hand-off's reactive watch: a small workflow on a
// cadence. Each tick it runs the FACT steps (read verbs, stored by id), then
// evaluates the ACTION steps in order (handoff.bail / handoff.rerun / a
// `workflow:` step, each `if:`-guarded); the first whose condition holds fires.
// Conditions see each fact step's output by id plus the frozen `.handoff.<id>`
// snapshot captured once at hand-off creation, so a rule can compare now vs then.
// bail tears the hand-off down and ends the loop; rerun/workflow SUPERSEDE it
// (tear down, then run, and the fresh run re-arms its own watch). The loop ends
// when its ctx is cancelled (the hand-off resolved) or an action fires.
func (e *Engine) startHandoffWatch(parent, runCtx context.Context, cancel context.CancelFunc, t core.Trigger, stepID, identity, agentID, prKey string, profile config.Step, ch handoff.Channel, actions dispatch.HandoffActions) {
	w := profile.Watch
	every := time.Duration(w.Every)
	if every <= 0 {
		every = 60 * time.Second
	}
	var factSteps, actionSteps []config.Step
	for _, st := range w.Steps {
		if st.Workflow != "" || strings.HasPrefix(st.Uses, "step.") || strings.HasPrefix(st.Uses, "handoff.") {
			actionSteps = append(actionSteps, st)
		} else {
			factSteps = append(factSteps, st)
		}
	}
	bail := func(reason string) {
		e.log("%s hand-off %q bailing: %s", tag(t), stepID, reason)
		cancel()                      // ends Review's Await → the loop goroutine returns
		e.broker.Close(parent, prKey) // close the draft/presentation
		e.hold.Remove(agentID)        // release the reaper hold → workspace reclaimed
	}
	go func() {
		defer e.recoverDispatch(parent, t, store.WorkflowRun{}, "hand-off watch")
		snap, err := e.runWatchFacts(runCtx, t, factSteps)
		if err != nil {
			// No baseline snapshot → comparisons against `.handoff.*` can't be
			// trusted; skip the watch rather than fire on a half-read.
			e.log("%s hand-off %q watch disabled: snapshot read failed: %v", tag(t), stepID, err)
			return
		}
		tk := time.NewTicker(every)
		defer tk.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-tk.C:
			}
			facts, err := e.runWatchFacts(runCtx, t, factSteps)
			if err != nil {
				continue // transient read failure — try again next tick
			}
			scope := e.stepBaseData(t)
			for id, out := range facts {
				scope[id] = out
			}
			scope["handoff"] = snap
			st, matched := matchWatch(actionSteps, scope, func(cond string, err error) {
				e.log("%s hand-off %q watch step %q eval error: %v", tag(t), stepID, cond, err)
			})
			if !matched {
				continue
			}
			switch {
			case st.Workflow != "":
				if actions.RunWorkflow == nil {
					e.log("%s hand-off %q watch: workflow action unavailable here (ignored)", tag(t), stepID)
					continue
				}
				wf, with := st.Workflow, st.With
				e.supersedeHandoff(parent, cancel, t, stepID, agentID, prKey,
					func(c context.Context) error { return actions.RunWorkflow(c, wf, with) })
				return
			case st.Uses == "step.bail" || st.Uses == "handoff.bail":
				reason := st.If
				if reason == "" {
					reason = "watch condition met"
				}
				bail(reason)
				return
			case st.Uses == "step.rerun" || st.Uses == "handoff.rerun":
				if actions.RerunStep == nil {
					e.log("%s hand-off %q watch: rerun unavailable here (ignored)", tag(t), stepID)
					continue
				}
				prompt, _ := st.Options["prompt"].(string)
				e.supersedeHandoff(parent, cancel, t, stepID, agentID, prKey,
					func(c context.Context) error { return actions.RerunStep(c, prompt) })
				return
			default:
				// validateWatch keeps other actions out; log defensively.
				e.log("%s hand-off %q watch: unsupported action %q (ignored)", tag(t), stepID, st.Uses)
			}
		}
	}()
}

// supersedeHandoff replaces a hand-off whose subject moved: it ends the current
// review and tears down the stale agent/workspace, then runs `run` — the watch
// rule's chosen action (rerun this step, or run a named workflow), which
// re-establishes a fresh hand-off itself. The teardown happens FIRST for every
// supersede action (the user's call), so a stale draft never coexists with its
// replacement.
func (e *Engine) supersedeHandoff(parent context.Context, cancel context.CancelFunc, t core.Trigger, stepID, oldAgentID, prKey string, run func(context.Context) error) {
	e.log("%s hand-off %q superseding on new state", tag(t), stepID)
	// Tear the current cycle down before running the replacement: end the review
	// loop, close the draft, release + archive the stale agent/workspace.
	cancel()
	if e.broker != nil {
		e.broker.Close(parent, prKey)
	}
	e.hold.Remove(oldAgentID)
	e.deregisterLiveHandoff(oldAgentID)
	if oldAgentID != "" && !e.affinityOwns(oldAgentID) {
		go func(id string) { _ = e.archiveAgent(context.Background(), id) }(oldAgentID)
	}
	// Run the replacement on the current state — lands a fresh hand-off.
	if err := run(parent); err != nil {
		e.log("%s hand-off %q supersede failed: %v", tag(t), stepID, e.redact(err.Error()))
	}
}

// matchWatch returns the first action step whose `if:` holds against scope (an
// empty `if:` always holds). An eval error skips that step (reported via onErr)
// rather than firing it — a broken condition must never tear a hand-off down.
func matchWatch(steps []config.Step, scope map[string]any, onErr func(cond string, err error)) (config.Step, bool) {
	for _, st := range steps {
		if strings.TrimSpace(st.If) == "" {
			return st, true
		}
		ok, err := expr.Eval(st.If, scope)
		if err != nil {
			if onErr != nil {
				onErr(st.If, err)
			}
			continue
		}
		if ok {
			return st, true
		}
	}
	return config.Step{}, false
}

// runWatchFacts runs the watch's fact steps (read verbs) and returns their
// outputs keyed by step id — the map exposed to conditions (and, captured once
// at creation, as the frozen `.handoff` snapshot). A read failure fails the tick
// so a half-read never drives an action.
func (e *Engine) runWatchFacts(ctx context.Context, t core.Trigger, facts []config.Step) (map[string]any, error) {
	out := map[string]any{}
	for _, st := range facts {
		o, err := e.readVerb(ctx, t, st.Uses, st.Options)
		if err != nil {
			return nil, fmt.Errorf("watch fact %q: %w", st.ID, err)
		}
		id := st.ID
		if id == "" {
			id = st.Uses
		}
		out[id] = o
	}
	return out, nil
}

// readVerb invokes a read verb for a watch fact step. It defaults repo/pr from
// the trigger target ONLY when the platform assigned it (TargetTrusted) — a
// forged target can't drive a watch off a PR the sender picked; the step's own
// options override the defaults.
func (e *Engine) readVerb(ctx context.Context, t core.Trigger, uses string, options map[string]any) (map[string]any, error) {
	conn, verb, ok := strings.Cut(uses, ".")
	if !ok || conn == "" || verb == "" {
		return nil, fmt.Errorf("watch fact `uses: %s` must be connector.verb", uses)
	}
	inst, found := e.connectors.Get(conn)
	if !found || inst == nil {
		return nil, fmt.Errorf("watch: unknown connector %q", conn)
	}
	opts := map[string]any{}
	if t.TargetTrusted {
		if t.Target.Repo != "" {
			opts["repo"] = t.Target.Repo
		}
		if t.Target.Number != 0 {
			opts["pr"] = t.Target.Number
		}
	}
	for k, v := range options {
		opts[k] = v
	}
	return inst.Invoke(ctx, verb, opts)
}

// tailOutput returns the last few non-blank lines of a command's captured output
// for the journal — the result (critique's verdict, a gh message, an error) is at
// the end, while the go-build/download preamble is at the start and gets dropped.
// The full output is always in the audit log. Returns "" when there's nothing.
func tailOutput(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < 8; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		kept = append([]string{strings.TrimRight(lines[i], " \t")}, kept...)
	}
	out := strings.Join(kept, "\n")
	if len(out) > 800 {
		out = "…" + out[len(out)-800:]
	}
	return out
}

// actionDesc is a short, journal-friendly label for what a step/action runs: the
// agent name for agent steps, or the (rendered-at-dispatch) command for commands.
func actionDesc(a config.Action) string {
	if len(a.Command) > 0 {
		s := strings.Join(a.Command, " ")
		if len(s) > 120 {
			s = s[:120] + "…"
		}
		return "command: " + s
	}
	if a.Agent != "" {
		return "agent: " + a.Agent
	}
	if a.Type != "" {
		return a.Type
	}
	return "action"
}

// logOutputs renders a step's outputs compactly for the journal (e.g. the assess
// step's {decision, reason}), so a misroute is visible in `journalctl` without
// digging through the audit log. Returns "" when there's nothing to show.
func logOutputs(outputs map[string]any) string {
	if len(outputs) == 0 {
		return ""
	}
	b, err := json.Marshal(outputs)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return " → " + s
}

// stepBaseData builds the template/condition data (trigger fields + context).
func (e *Engine) stepBaseData(t core.Trigger) map[string]any {
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

// extractOutputs parses a step's captured output into an outputs map. JSON
// objects become the outputs (unwrapping a common paseo wrapper key); anything
// else is exposed as `.text`.
func extractOutputs(ref dispatch.RunRef) map[string]any {
	out := strings.TrimSpace(ref.Output)
	if out == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err == nil {
		for _, k := range []string{"output", "result", "outputs"} {
			if inner, ok := m[k].(map[string]any); ok {
				return inner
			}
		}
		return m
	}
	return map[string]any{"text": out}
}
