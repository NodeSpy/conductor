package flow

import (
	"context"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Quality gates (#36 §16): after a foreground agent step finishes, its
// PROPOSED change (the worktree it worked in) is checked by the gate's named
// checks — tests, lint/build, a critic-agent verdict, any verb with a
// pass/fail reading — before the step's outputs promote into the run. A
// failure loops back to the SAME agent as a follow-up carrying the failing
// checks' detail (bounded by max_revisions), then escalates: needs_input
// notification, audited, step failed. "The agent did something" becomes
// "the agent did something that passes."
//
// Checks are ordinary steps from the top-level `checks:` map, executed
// through the same step machinery with the agent's worktree as their default
// working directory and a `gate` scope block ({{.gate.workdir}},
// {{.gate.attempt}}, {{.gate.output}}) for verbs/critics that want context.
//
// Pass/fail reading: a step error is a fail; an explicit `pass: false`
// output is a fail; an agent (critic) check MUST output `pass` explicitly
// (a critic that doesn't render a verdict is a fail, not a shrug); anything
// else that completes is a pass.

// gateKey carries the workflow-scope default gate through the step tree.
type gateKey struct{}

// inGateKey marks execution INSIDE a gate check — a critic agent step never
// gets gated itself (no recursion).
type inGateKey struct{}

func inGate(ctx context.Context) bool {
	on, _ := ctx.Value(inGateKey{}).(bool)
	return on
}

// withDefaultGate stashes a trigger/workflow-level gate as the default for
// its agent steps (a step's own gate: wins — see effectiveGate).
func withDefaultGate(ctx context.Context, g *config.GateSpec) context.Context {
	if g == nil {
		return ctx
	}
	return context.WithValue(ctx, gateKey{}, g)
}

// effectiveGate resolves the gate governing one agent step.
func (r *Runner) effectiveGate(ctx context.Context, step config.Step) *config.GateSpec {
	if inGate(ctx) {
		return nil
	}
	if step.Gate != nil {
		return step.Gate
	}
	g, _ := ctx.Value(gateKey{}).(*config.GateSpec)
	return g
}

// gateCheckResult is one check's verdict.
type gateCheckResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"` // failure detail for the revise prompt
}

// runGate runs one agent step's gate to a verdict. nil = promoted (all
// checks pass, possibly after revisions); an error discards the step.
// rounds reports how many revise rounds it took.
func (r *Runner) runGate(ctx context.Context, t core.Trigger, step config.Step, id string, spec *config.GateSpec, ref dispatch.RunRef, data map[string]any) (rounds int, err error) {
	ctx = context.WithValue(ctx, inGateKey{}, true)
	maxRev := spec.MaxRevisionsOrDefault()
	for round := 0; ; round++ {
		failures := r.runGateChecks(ctx, t, id, spec, ref, data, round)
		if len(failures) == 0 {
			r.audit(map[string]any{"event": "gate", "repo": t.Target.Repo, "number": t.Target.Number,
				"kind": t.Kind, "step": id, "agent": step.Agent, "round": round, "outcome": "pass"})
			histFrom(ctx).emit("gate", id, "pass", "", 0)
			return round, nil
		}
		names := failureNames(failures)
		r.audit(map[string]any{"event": "gate", "repo": t.Target.Repo, "number": t.Target.Number,
			"kind": t.Kind, "step": id, "agent": step.Agent, "round": round,
			"failed": names, "outcome": "fail"})
		histFrom(ctx).emit("gate", id, "fail", strings.Join(names, ", "), 0)
		if round >= maxRev {
			return round, r.escalateGate(ctx, t, step, id, failures, round)
		}
		out, ok, ferr := r.followUp(ctx, t, step, ref, r.revisePrompt(failures, round, maxRev))
		if ferr != nil || !ok {
			if ferr != nil {
				r.Log("%s gate %s: revise follow-up failed: %v", flowTag(t), id, ferr)
			}
			return round, r.escalateGate(ctx, t, step, id, failures, round)
		}
		_ = out // the agent revises IN the worktree; the re-run checks are the judge
		r.audit(map[string]any{"event": "gate", "repo": t.Target.Repo, "number": t.Target.Number,
			"kind": t.Kind, "step": id, "agent": step.Agent, "round": round,
			"failed": names, "outcome": "revise"})
		histFrom(ctx).emit("gate", id, "revise", "", 0)
	}
}

func failureNames(fails []gateCheckResult) []string {
	names := make([]string, len(fails))
	for i, f := range fails {
		names[i] = f.Name
	}
	return names
}

// escalateGate is the discard posture: notify a human, audit, fail the step.
func (r *Runner) escalateGate(ctx context.Context, t core.Trigger, step config.Step, id string, fails []gateCheckResult, round int) error {
	names := strings.Join(failureNames(fails), ", ")
	msg := fmt.Sprintf("gate on step %q failed (%s) after %d revision round(s) — change NOT promoted; review agent %s's worktree", id, names, round, step.Agent)
	if r.Notif != nil {
		r.Notif.Emit(ctx, "needs_input", t, msg)
	}
	r.audit(map[string]any{"event": "gate", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "step": id, "agent": step.Agent, "round": round,
		"failed": failureNames(fails), "outcome": "escalated"})
	histFrom(ctx).emit("gate", id, "escalated", names, 0)
	return fmt.Errorf("gate failed: %s (after %d revision round(s))", names, round)
}

// runGateChecks executes every check (in declared order) and returns the
// failures. All checks run even after one fails — the revise prompt carries
// the complete picture.
func (r *Runner) runGateChecks(ctx context.Context, t core.Trigger, stepID string, spec *config.GateSpec, ref dispatch.RunRef, data map[string]any, round int) []gateCheckResult {
	var failures []gateCheckResult
	for _, name := range spec.Run {
		chk, ok := r.Cfg.Checks[name]
		if !ok {
			// A team step's ephemeral checks (the critic) ride the context.
			chk, ok = teamCheck(ctx, name)
		}
		if !ok { // load-validated; belt for dynamic plans
			failures = append(failures, gateCheckResult{Name: name, Detail: "unknown check"})
			continue
		}
		res := r.execCheck(ctx, t, stepID, name, chk, ref, data, round)
		r.Log("%s gate %s: check %s → %s", flowTag(t), stepID, name, passWord(res.Pass))
		if !res.Pass {
			failures = append(failures, res)
		}
	}
	return failures
}

func passWord(p bool) string {
	if p {
		return "pass"
	}
	return "FAIL"
}

// execCheck runs one check step against the agent's worktree and reads its
// verdict.
func (r *Runner) execCheck(ctx context.Context, t core.Trigger, stepID, name string, chk config.Step, ref dispatch.RunRef, data map[string]any, round int) gateCheckResult {
	// Checks that examine the proposed change run IN the agent's worktree. A
	// check may pin its own workdir (or host) instead; otherwise a missing
	// local worktree (remote runtime, checkout: none) is a clear FAILURE for
	// every form — command, code, AND an agent critic (#36 review H6): a
	// critic dispatched with nothing to review can still emit pass: true,
	// which would be a silent pass on an unreviewed change.
	needsWorkdir := (chk.Form() == "command" || chk.Form() == "code" || chk.Form() == "agent") &&
		chk.WorkDir == "" && chk.Host == "" && chk.SSH == nil
	if needsWorkdir && ref.Workdir == "" {
		return gateCheckResult{Name: name, Detail: "no local worktree to check (remote runtime or checkout: none) — give the check an explicit workdir:/host:, or run the agent on a local worktree"}
	}
	if needsWorkdir {
		chk.WorkDir = ref.Workdir
		// An agent critic reviews the existing worktree in place — never
		// provision a second checkout.
		if chk.Form() == "agent" && chk.Checkout == "" {
			chk.Checkout = "none"
		}
	}

	cdata := cloneData(data)
	cdata["gate"] = map[string]any{
		"step":    stepID,
		"check":   name,
		"workdir": ref.Workdir,
		"attempt": round + 1,
		"output":  clipText(ref.Output, 4000),
	}
	outputs, err := r.execStepWithFlow(ctx, t, chk, "gate:"+name, cdata, false)
	if err != nil {
		return gateCheckResult{Name: name, Detail: r.redactErr(err)}
	}
	if p, declared := outputs["pass"].(bool); declared {
		if !p {
			return gateCheckResult{Name: name, Detail: verdictDetail(outputs)}
		}
		return gateCheckResult{Name: name, Pass: true}
	}
	if chk.Form() == "agent" {
		return gateCheckResult{Name: name, Detail: "critic returned no explicit pass: true|false verdict — declare it in output_schema and instruct the critic to emit it"}
	}
	return gateCheckResult{Name: name, Pass: true}
}

// verdictDetail extracts the human-facing failure detail from a check's
// outputs (reason/detail/text, else a compact dump).
func verdictDetail(outputs map[string]any) string {
	for _, k := range []string{"reason", "detail", "text", "stderr", "stdout"} {
		if s, ok := outputs[k].(string); ok && strings.TrimSpace(s) != "" {
			return clipText(s, 2000)
		}
	}
	return "check reported pass: false"
}

// revisePrompt renders the failing checks into the follow-up the agent gets.
func (r *Runner) revisePrompt(fails []gateCheckResult, round, maxRev int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your proposed change FAILED its quality gate (revision %d of %d). Failing checks:\n\n", round+1, maxRev)
	for _, f := range fails {
		fmt.Fprintf(&b, "### %s\n%s\n\n", f.Name, f.Detail)
	}
	b.WriteString("Fix the change in your working directory (amend/commit as appropriate). " +
		"The same checks re-run when you finish; reply briefly with what you changed.")
	return r.redactText(b.String())
}

// redactText scrubs tracked secrets from gate-composed text.
func (r *Runner) redactText(s string) string {
	if r.Secrets == nil {
		return s
	}
	return r.Secrets.Redact(s)
}

// followUp routes the revise prompt back to the SAME agent: the engine
// resolves the transport (bound session, paseo send-capture) — ok=false when
// the runtime can't take a follow-up, which escalates instead of revising.
func (r *Runner) followUp(ctx context.Context, t core.Trigger, step config.Step, ref dispatch.RunRef, prompt string) (string, bool, error) {
	if r.Agents.FollowUp == nil {
		return "", false, nil
	}
	return r.Agents.FollowUp(ctx, ref.AgentID, step.Agent, t, prompt)
}
