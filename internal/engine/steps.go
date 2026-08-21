package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/expr"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
	"github.com/NodeSpy/paseo-conductor/internal/store"
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
				if s.RerequestReview {
					s.Prompt += dispatch.RerequestReviewGuidance
				}
				if profile.ArchiveWhenDone {
					s.Prompt += dispatch.HoldGuidance
				}
			}
		}
		req := dispatch.Request{
			Trigger: t, Action: s, Profile: profile,
			Tokens: dispatch.Tokens{App: appTok, User: userTok},
			Author: e.author, Shadow: shadow, Wait: !s.Background, Data: data,
		}
		e.log("%s step %s running (%s)", tag(t), id, actionDesc(s))
		start := time.Now()
		ref, err := e.disp.Dispatch(ctx, req)
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
			// Handed off to a live agent — tell the user it's waiting for them.
			e.log("%s step %s launched in background after %s (agent %s)", tag(t), id, took, ref.AgentID)
			e.notif.Emit(ctx, notify.EventNeedsInput, t,
				fmt.Sprintf("interactive agent for %q is live in paseo (agent %s) — open it to review/refine", id, ref.AgentID))
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
	}
	e.notif.Emit(ctx, notify.EventComplete, t, "workflow")
	e.finishRun(run)
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
