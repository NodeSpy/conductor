package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/expr"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
)

// runSteps executes a multi-step workflow: each step may use a different
// agent/model, gate on an `if` condition over prior outputs, and (agent steps)
// emit structured output that later steps reference via
// {{ .steps.<id>.outputs.<key> }} and `if` conditions like
// `steps.<id>.outputs.<key> == true`. Steps run to completion in order; the
// whole workflow runs in its own goroutine so the engine loop isn't blocked.
func (e *Engine) runSteps(ctx context.Context, t core.Trigger, act config.Action, appTok, userTok string, shadow bool) {
	data := e.stepBaseData(t)
	stepsOut := map[string]any{}
	data["steps"] = stepsOut

	for i, step := range act.Steps {
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("step%d", i+1)
		}

		if step.If != "" {
			ok, err := expr.Eval(step.If, data)
			if err != nil {
				e.log("engine: step %s if-error: %v", id, err)
				e.store.Audit(map[string]any{"event": "step_error", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "error": err.Error()})
				return
			}
			if !ok {
				e.store.Audit(map[string]any{"event": "step_skipped", "repo": t.Target.Repo,
					"number": t.Target.Number, "kind": t.Kind, "step": id, "if": step.If})
				continue
			}
		}

		s := step
		var profile config.AgentProfile
		if s.Type == "agent" {
			profile = e.cfg.Agents[s.Agent]
			if s.Prompt != "" {
				s.Prompt += dispatch.WriteWrapperGuidance
			}
		}
		req := dispatch.Request{
			Trigger: t, Action: s, Profile: profile,
			Tokens: dispatch.Tokens{App: appTok, User: userTok},
			Author: e.author, Shadow: shadow, Wait: true, Data: data,
		}
		ref, err := e.disp.Dispatch(ctx, req)
		outputs := extractOutputs(ref)
		stepsOut[id] = map[string]any{"outputs": outputs}

		entry := map[string]any{"event": "step", "repo": t.Target.Repo, "number": t.Target.Number,
			"kind": t.Kind, "step": id, "backend": ref.Backend, "shadow": ref.Shadowed}
		if err != nil {
			entry["error"] = err.Error()
			e.store.Audit(entry)
			e.log("engine: step %s failed: %v", id, err)
			e.notif.Emit(ctx, notify.EventEscalate, t, fmt.Sprintf("workflow step %q failed: %v", id, err))
			return // fail-fast
		}
		e.store.Audit(entry)
		e.log("engine: step %s done (%s)", id, ref.Backend)
	}
	e.notif.Emit(ctx, notify.EventComplete, t, "workflow")
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
