package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/decider"
	"github.com/NodeSpy/conductor/internal/expr"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/systemone"
)

// decide: steps (config.DecideSpec). The step's `model:` resolves to a ranked
// list of (runtime, model) candidates — native decision runtimes first, then
// agent runtimes answering through conductor's system_one/v1 adapter — and
// execution walks it:
//
//  1. Ask each candidate in order until one answers. A failure (an error, a
//     timeout, an invalid answer) moves to the next; a successful answer is
//     final whatever its confidence.
//  2. When every candidate failed, `default:` answers, or the step fails.
//  3. When the step declares `escalate:` and its `when:` holds over the
//     answer, ask the next candidate (or escalate.to's), up to escalate.max
//     hops. The newer answer replaces the older, which is kept in
//     `_escalated_from` for audit.
//  4. With `observe:` set, every answer (initial and escalated) is recorded
//     to that SQL store.
//
// The outputs are the v1 `answers` object itself — one entry per question —
// plus `_by` (the "<runtime>/<model>" that answered, or "default"), and
// `_escalated_from` when an escalation replaced an earlier answer.

// decideDefaultBy is the `_by` of answers that came from `default:`.
const decideDefaultBy = "default"

// decideAnswer is one answer and who gave it.
type decideAnswer struct {
	answers map[string]any
	by      string
	native  bool
}

// execDecide runs a decide: step.
func (r *Runner) execDecide(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any, shadow bool) (map[string]any, string, error) {
	d := step.Decide
	protocol := d.ProtocolOrDefault()
	state, err := renderValue(d.State, data)
	if err != nil {
		return nil, "", fmt.Errorf("decide.state: %w", err)
	}
	req := systemone.Request{State: state, Questions: d.Questions}
	historySetInputs(ctx, id, map[string]any{"decide": d.Questions.Names(), "protocol": protocol})

	if shadow || r.DryRun {
		return r.decideStub(d), "", nil
	}

	cands, err := r.decideCandidates(ctx, step, step.Model, protocol)
	if err != nil {
		return nil, "", fmt.Errorf("decide: %w", err)
	}
	asked := map[string]bool{}
	var history []decideAnswer

	first, idx, err := r.answerFrom(ctx, t, step, id, slot, data, protocol, req, cands, 0, asked)
	if err != nil {
		if d.Default == nil {
			return nil, "", fmt.Errorf("decide: no candidate answered: %w", err)
		}
		defaults, derr := systemone.DefaultAnswers(d.Questions, d.Default)
		if derr != nil {
			// Validated at load; reaching here means the config changed under us.
			return nil, "", fmt.Errorf("decide.default: %w", derr)
		}
		r.Log("%s decide %s: every candidate failed (%v) — using default:", flowTag(t), id, err)
		cur := decideAnswer{answers: defaults, by: decideDefaultBy}
		r.observeDecisions(ctx, t, step, id, []decideAnswer{cur})
		return decideOutputs(cur, nil), "", nil
	}
	history = append(history, first)
	outputs := decideOutputs(first, nil)

	if e := d.Escalate; e != nil {
		pool, next := cands, idx+1
		retargeted := false
		cur := first
		for hop := 0; hop < e.Hops(); hop++ {
			uncertain, eerr := expr.Eval(e.When, cur.answers)
			if eerr != nil {
				r.Log("%s decide %s: escalate.when: %v — keeping the answer", flowTag(t), id, eerr)
				break
			}
			if !uncertain {
				break
			}
			if e.To.Set() && !retargeted {
				// escalate.to is its own tier: rank it once, then keep walking
				// down it on later hops.
				p, perr := r.decideCandidates(ctx, step, e.To, protocol)
				if perr != nil {
					r.Log("%s decide %s: escalate.to: %v — keeping the answer", flowTag(t), id, perr)
					break
				}
				pool, next, retargeted = p, 0, true
			}
			ans, at, aerr := r.answerFrom(ctx, t, step, id, slot, data, protocol, req, pool, next, asked)
			if aerr != nil {
				r.Log("%s decide %s: escalation found no second answer (%v) — keeping %s's", flowTag(t), id, aerr, cur.by)
				break
			}
			r.audit(map[string]any{"event": "decide_escalated", "repo": t.Target.Repo, "number": t.Target.Number,
				"step": id, "from": cur.by, "to": ans.by, "hop": hop + 1})
			outputs = decideOutputs(ans, outputs)
			history = append(history, ans)
			cur, next = ans, at+1
		}
	}
	r.observeDecisions(ctx, t, step, id, history)
	return outputs, "", nil
}

// decideOutputs is one answer as step outputs: the answers themselves plus
// `_by`, and — when this answer replaced an earlier one — `_escalated_from`
// carrying the earlier answer (and, on a chain, its own predecessor).
func decideOutputs(a decideAnswer, prev map[string]any) map[string]any {
	out := make(map[string]any, len(a.answers)+2)
	for k, v := range a.answers {
		out[k] = v
	}
	out["_by"] = a.by
	if prev != nil {
		from := map[string]any{"_by": prev["_by"], "answers": answersOf(prev)}
		if older, ok := prev["_escalated_from"]; ok {
			from["_escalated_from"] = older
		}
		out["_escalated_from"] = from
	}
	return out
}

// answersOf strips conductor's `_` fields off a decide step's outputs,
// leaving the answers.
func answersOf(outputs map[string]any) map[string]any {
	out := make(map[string]any, len(outputs))
	for k, v := range outputs {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	return out
}

// decideStub is a shadow/dry-run decide step's outputs: its declared
// defaults when it has them (so a preview downstream sees the right shape),
// else nothing but the marker.
func (r *Runner) decideStub(d *config.DecideSpec) map[string]any {
	out := map[string]any{"_by": "shadow", "stubbed": true}
	if d.Default != nil {
		if defaults, err := systemone.DefaultAnswers(d.Questions, d.Default); err == nil {
			for k, v := range defaults {
				out[k] = v
			}
		}
	}
	return out
}

// decideCandidates ranks the candidates for a model spec. With no resolver
// wired (a test, a bare runner) the only candidate is the step's own agent
// resolution.
func (r *Runner) decideCandidates(ctx context.Context, step config.Step, spec config.ModelSpec, protocol string) ([]models.Candidate, error) {
	if r.Agents.DecideCandidates != nil {
		cands, err := r.Agents.DecideCandidates(ctx, spec, step.Runtime, protocol)
		if err != nil {
			return nil, err
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("no runtime can answer this decision")
		}
		return cands, nil
	}
	c := models.Candidate{Runtime: step.Runtime}
	if r.Agents.ResolveModel != nil {
		probe := step
		probe.Model = spec
		model, rt, provider := r.Agents.ResolveModel(ctx, probe)
		c.Model, c.Provider = model, provider
		if rt != "" {
			c.Runtime = rt
		}
		c.Bare = model == ""
	}
	return []models.Candidate{c}, nil
}

// answerFrom asks pool[start:] in order, skipping pairs already asked in this
// step, and returns the first valid answer with its index. Every failure is
// logged and audited; the error returned is the last one, when none answered.
func (r *Runner) answerFrom(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any,
	protocol string, req systemone.Request, pool []models.Candidate, start int, asked map[string]bool) (decideAnswer, int, error) {
	var lastErr error
	for i := start; i < len(pool); i++ {
		c := pool[i]
		label := c.Label()
		if asked[label] {
			continue
		}
		asked[label] = true
		if err := ctx.Err(); err != nil {
			return decideAnswer{}, i, err
		}
		ans, err := r.answerOne(ctx, t, step, id, slot, data, protocol, req, c)
		r.auditDecide(t, id, c, err)
		if err != nil {
			lastErr = err
			r.Log("%s decide %s: %s did not answer: %v", flowTag(t), id, label, err)
			continue
		}
		return ans, i, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate left to ask")
	}
	return decideAnswer{}, len(pool), lastErr
}

// answerOne asks one candidate. A native runtime gets the protocol request
// as-is; an agent runtime gets the adapter's rendering in one restricted
// session — no checkout, no guidance of the decide step's own, the prompt
// and schema only — and its reply is converted back into v1 answers.
func (r *Runner) answerOne(ctx context.Context, t core.Trigger, step config.Step, id, slot string, data map[string]any,
	protocol string, req systemone.Request, c models.Candidate) (decideAnswer, error) {
	if c.Native {
		if r.Agents.Decide == nil {
			return decideAnswer{}, fmt.Errorf("decision runtime %q is not wired in this process", c.Runtime)
		}
		q := req
		q.Model = c.Model
		a, err := r.Agents.Decide(ctx, c.Runtime, protocol, q)
		if err != nil {
			return decideAnswer{}, err
		}
		by := models.Candidate{Runtime: c.Runtime, Model: a.Model}.Label()
		return decideAnswer{answers: a.Answers, by: by, native: true}, nil
	}
	agent := config.Step{
		ID:   step.ID,
		Name: step.Name,
		Type: "agent",
		// The candidate IS the resolution: pin it, so the dispatch runs
		// exactly the pair that was ranked (an empty model is the bare
		// launch the ranking chose).
		Model:        config.ModelSpecOf(c.Model),
		Runtime:      c.Runtime,
		Prompt:       systemone.Prompt(req.State, req.Questions),
		Checkout:     "none",
		OutputSchema: systemone.OutputSchema(req.Questions),
		// The parts, for a runtime that runs a lean decision session — and
		// the marker that keeps this prompt out of templating.
		DecisionLaunch: &config.DecisionLaunch{
			System:   systemone.SystemPrompt(),
			Document: systemone.UserPrompt(req.State, req.Questions),
			Schema:   systemone.OutputSchema(req.Questions),
		},
		Timeout: step.Timeout,
		// One turn, then gone: a decision holds no session worth keeping.
		ArchiveWhenDone: true,
	}
	out, _, err := r.execAgent(ctx, t, agent, id, slot, data, false)
	if err != nil {
		return decideAnswer{}, err
	}
	answers, err := systemone.FromAgentOutput(req.Questions, out)
	if err != nil {
		return decideAnswer{}, fmt.Errorf("the agent's reply is not a valid decision: %w", err)
	}
	return decideAnswer{answers: answers, by: c.Label()}, nil
}

// auditDecide records one candidate attempt.
func (r *Runner) auditDecide(t core.Trigger, step string, c models.Candidate, err error) {
	entry := map[string]any{
		"event": "decide", "repo": t.Target.Repo, "number": t.Target.Number,
		"step": step, "runtime": c.Runtime, "model": c.Model, "native": c.Native,
		"outcome": "answered",
	}
	if err != nil {
		entry["outcome"] = "failed"
		entry["error"] = r.redactErr(err)
	}
	r.audit(entry)
}

// DecideAnswer is a native decision runtime's reply, as the flow runner
// receives it from the engine's Decide service.
type DecideAnswer = decider.Answer
