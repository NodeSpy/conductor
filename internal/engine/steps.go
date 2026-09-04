package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/expr"
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
		var profile config.AgentProfile
		if s.Type == "agent" {
			profile = e.cfg.Agents[s.Agent]
			if s.Background {
				// A background step hands off a live agent for you to drive and
				// close yourself; it sits idle *because* it's waiting for you, so
				// the reaper must never archive it. Force this regardless of the
				// profile — a stale-on-disk or mistaken archive_when_done: true
				// must not be able to reap an interactive hand-off out from under you.
				profile.ArchiveWhenDone = false
			}
			if s.Prompt != "" {
				s.Prompt += dispatch.WriteWrapperGuidance
				s.Prompt += e.agentGuidance(profile)
				if s.RerequestReview {
					s.Prompt += dispatch.RerequestReviewGuidance
				}
				// Only the interactive hand-off (a background step) is told to ask. A
				// non-background step runs autonomously — especially a schema step like
				// `assess`, which MUST produce its structured output and must never pause
				// asking (that surfaces as "waiting for permission" and fails the schema).
				if s.Background {
					s.Prompt += dispatch.HandoffGuidance
				}
			}
		}
		req := dispatch.Request{
			Trigger: t, Action: s, Profile: profile,
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
		e.log("%s step %s running (%s)", tag(t), id, actionDesc(s))
		start := time.Now()
		ref, err := runner.Dispatch(ctx, req)
		// A step that exited cleanly but reports it isn't done yet (e.g. critique
		// deferring on pending CI) is retried per its `retry:` policy — the workflow
		// won't complete a not-ready step.
		if err == nil && !s.Background && s.Retry != nil {
			ref = e.retryWhileDeferred(ctx, req, ref, s.Retry)
		}
		took := time.Since(start).Round(time.Second)
		// A background step launches a live agent and returns immediately; there's
		// no captured output to fold into later steps.
		outputs := map[string]any{}
		if !s.Background {
			outputs = extractOutputs(ref)
		}
		stepsOut[id] = map[string]any{"outputs": outputs}

		entry := map[string]any{"event": "step", "repo": t.Target.Repo, "number": t.Target.Number,
			"kind": t.Kind, "step": id, "backend": ref.Backend, "shadow": ref.Shadowed}
		if err != nil {
			entry["error"] = err.Error()
			e.store.Audit(entry)
			failMsg := ""
			if tail := tailOutput(ref.Output); tail != "" {
				failMsg = "\n" + tail
			}
			e.log("%s step %s failed after %s: %v%s", tag(t), id, took, err, failMsg)
			e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("workflow step %q failed: %v", id, err))
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
				e.startReviewHandoff(ctx, t, id, profile, ref.AgentID, handoffCh)
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
		if s.Type == "agent" && profile.ArchiveWhenDone && ref.AgentID != "" {
			go func(id string) { _ = e.disp.Archive(context.Background(), id) }(ref.AgentID)
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
func (e *Engine) startReviewHandoff(ctx context.Context, t core.Trigger, stepID string, profile config.AgentProfile, agentID string, ch handoff.Channel) {
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
	sess, err := c.ResumeSession(ctx, agentID, handler)
	if err != nil {
		fallback(fmt.Sprintf("bind agent %s: %v", agentID, err))
		return
	}
	e.broker.Bind(prKey, c, sess)
	draft := handoff.Draft{
		Title:  fmt.Sprintf("Review for %s", prKey),
		Body:   "The agent is preparing its review. Edit the text and choose Send revision to hand it back to the agent, Approve to have it submit as-is, or Discard.",
		PRKey:  prKey,
		Repo:   t.Target.Repo,
		Number: t.Target.Number,
	}
	go func() {
		dec, rerr := handoff.Review(ctx, sess, ch, draft, notifyRef)
		if rerr != nil {
			if ctx.Err() == nil {
				e.log("%s review hand-off loop for %q ended: %v", tag(t), stepID, rerr)
			}
			return
		}
		e.log("%s review hand-off for %q resolved: %s", tag(t), stepID, dec.Action)
		if dec.Action == handoff.ActionDiscard {
			e.broker.Close(ctx, prKey)
		}
	}()
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
