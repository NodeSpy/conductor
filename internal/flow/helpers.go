package flow

import (
	"context"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/expr"
)

// HELPER STEPS — the forms conductor executes itself: no dispatch identity,
// no runtime, no gate, no connector, nothing to render. See
// internal/config/helpers.go for the family and what adding one costs.
//
// execHelper is the ONE seam the runner branches on (execStep calls it for
// any step.IsHelper()), so a second helper is a case here and a case there,
// not a new arm threaded through the step dispatcher, the plan validator and
// the guard. Helpers share the non-agent step contract exactly as the code
// and verb forms do: they return outputs recorded under `{{.steps.<id>.outputs}}`,
// an empty raw string (there is no text output for retry's
// while_output_matches to match), and an error that fails the step — so
// `id:`, `if:`, `for_each:`, hooks, checkpointing and the history record all
// work without knowing a helper exists.
func (r *Runner) execHelper(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
	switch step.HelperForm() {
	case config.HelperSleep:
		return r.execSleep(ctx, t, step, shadow)
	case config.HelperLog:
		return r.execLog(t, step, data, shadow)
	case config.HelperSet:
		return r.execSet(step, data)
	case config.HelperAssert:
		return r.execAssert(step, data)
	case config.HelperFail:
		return r.execFail(step, data)
	case config.HelperWaitFor:
		return r.execWaitFor(ctx, t, step, id, data, shadow)
	}
	// Unreachable through the loader (config.HelperForm and this switch list
	// the same set) — it fires only for a helper added to the config side and
	// not to this one, which is a build-time mistake worth naming out loud.
	return nil, "", fmt.Errorf("step %q: helper %q has no runner", id, step.HelperForm())
}

// execLog renders a message and drops it in the run log — a flow's own
// breadcrumb, no connector behind it. It has no outputs (like sleep, a step
// that invented a field would put it in every downstream scope) and runs the
// same in shadow, only tagged, since logging is already side-effect-free.
func (r *Runner) execLog(t core.Trigger, step config.Step, data map[string]any, shadow bool) (map[string]any, string, error) {
	msg, err := render(step.Log, data)
	if err != nil {
		return nil, "", fmt.Errorf("log: %w", err)
	}
	if shadow {
		r.Log("%s [dry-run] %s", flowTag(t), msg)
	} else {
		r.Log("%s %s", flowTag(t), msg)
	}
	return map[string]any{}, "", nil
}

// execSet publishes computed values as the step's outputs. Each value is
// rendered with types preserved (renderValue resolves a sole `{{.a.b}}` to the
// underlying typed value, not its string form), so a `set:` that lifts a list
// or number out of an earlier step round-trips intact. recordOutputs (in
// runSteps) addresses them at {{.<id>.<key>}} — which is why a `set:` step
// wants an `id:`. Pure render, no side effect, so it runs in shadow too: a dry
// run's later steps should see the same variables a real one would.
func (r *Runner) execSet(step config.Step, data map[string]any) (map[string]any, string, error) {
	out := make(map[string]any, len(step.Set))
	for k, v := range step.Set {
		rv, err := renderValue(v, data)
		if err != nil {
			return nil, "", fmt.Errorf("set %q: %w", k, err)
		}
		out[k] = rv
	}
	return out, "", nil
}

// execAssert fails the step (and the run) unless the condition is truthy,
// using the same expr grammar and truthiness as `if:`. It evaluates in shadow
// too — `if:` does, and an assertion is the same kind of flow-logic check a
// dry run is meant to exercise.
func (r *Runner) execAssert(step config.Step, data map[string]any) (map[string]any, string, error) {
	ok, err := expr.Eval(step.Assert, data)
	if err != nil {
		return nil, "", fmt.Errorf("assert: %w", err)
	}
	if !ok {
		return nil, "", fmt.Errorf("assertion failed: %s", step.Assert)
	}
	return map[string]any{}, "", nil
}

// execFail stops the run with a rendered message, unconditionally — guard it
// with `if:` for a conditional abort. It needs no expr: the decision to fail
// is the presence of the step (behind whatever `if:`/`for_each:` gate it sits).
func (r *Runner) execFail(step config.Step, data map[string]any) (map[string]any, string, error) {
	msg, err := render(step.Fail, data)
	if err != nil {
		return nil, "", fmt.Errorf("fail: %w", err)
	}
	return nil, "", fmt.Errorf("%s", msg)
}

// execWaitFor polls a read verb until its outputs satisfy `until:` or the
// timeout elapses — the principled form of a bare `sleep:` before a
// state-dependent step. Each poll runs the verb exactly as a `uses:` step
// would (options templated per attempt), overlays its outputs onto the run
// scope so the condition reads them top-level, and evaluates `until:` with the
// same expr engine as `if:`. The whole wait rides a child context with the
// timeout, so the bound is enforced by ctx.Done() (via r.sleep and the verb
// call), never a wall-clock read in the loop — a run that is shut down drops
// out immediately, and the deadline is just another Done().
func (r *Runner) execWaitFor(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
	wf := step.WaitFor
	synth := config.Step{Uses: wf.Uses, Options: wf.Options}
	every := wf.Every.D()
	if every <= 0 {
		every = 10 * time.Second
	}
	timeout := wf.Timeout.D()

	poll := func(pctx context.Context) (map[string]any, bool, error) {
		out, err := r.execVerb(pctx, t, synth, id, data, shadow)
		if err != nil {
			return nil, false, err
		}
		ok, err := expr.Eval(wf.Until, overlayScope(data, out))
		if err != nil {
			return nil, false, fmt.Errorf("until: %w", err)
		}
		return out, ok, nil
	}

	// Shadow: one stubbed poll, no wait — say what it would do and return the
	// stub outputs so later dry-run steps have something to read.
	if shadow {
		out, _, err := poll(ctx)
		if err != nil {
			return nil, "", err
		}
		r.Log("%s [dry-run] would poll %s until %q (every %s, timeout %s)", flowTag(t), wf.Uses, wf.Until, every, timeout)
		return out, "", nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// timedOut distinguishes OUR deadline (fail with a clear message) from a
	// parent cancellation/shutdown (propagate ctx.Err() untouched).
	timedOut := func() bool { return waitCtx.Err() != nil && ctx.Err() == nil }
	for {
		out, ok, err := poll(waitCtx)
		if err != nil {
			if timedOut() {
				return nil, "", fmt.Errorf("wait_for %s: timed out after %s waiting for %q", wf.Uses, timeout, wf.Until)
			}
			return nil, "", err
		}
		if ok {
			return out, "", nil
		}
		if err := r.sleep(waitCtx, every); err != nil {
			if timedOut() {
				return nil, "", fmt.Errorf("wait_for %s: timed out after %s waiting for %q", wf.Uses, timeout, wf.Until)
			}
			return nil, "", err
		}
	}
}

// overlayScope returns a shallow copy of the run scope with a poll's outputs
// laid over the top, so a `wait_for` condition can name a verb's output field
// directly (`until: "status == 'completed'"`) while still seeing every fact
// and prior-step output. The copy is per-poll and thrown away — the run scope
// is never mutated by a wait.
func overlayScope(data, out map[string]any) map[string]any {
	scope := make(map[string]any, len(data)+len(out))
	for k, v := range data {
		scope[k] = v
	}
	for k, v := range out {
		scope[k] = v
	}
	return scope
}

// execSleep pauses the flow for the step's duration.
//
// It waits on r.sleep — the same ctx-aware timer the retry backoff uses, a
// time.Timer raced against ctx.Done() — so the wait is min(duration,
// whatever is left of the context) and a run that is shut down, budget-cut or
// timed out drops out of the sleep immediately with the context's error
// instead of holding a goroutine (and the run) for the full duration. A bare
// time.Sleep here would make `sleep: 30m` a thirty-minute shutdown.
//
// Outputs are empty: there is nothing to report, and a step that invented a
// field would put it in every template scope downstream. Under shadow the
// step does NOT wait — it says what it would have done and returns the same
// {"stubbed": true} marker the other dry-run step forms return, so a dry run
// stays as fast as it is honest.
func (r *Runner) execSleep(ctx context.Context, t core.Trigger, step config.Step, shadow bool) (map[string]any, string, error) {
	d := step.Sleep.D()
	if shadow {
		r.Log("%s [dry-run] would sleep %s", flowTag(t), d)
		return map[string]any{"stubbed": true}, "", nil
	}
	if err := r.sleep(ctx, d); err != nil {
		return nil, "", err
	}
	return map[string]any{}, "", nil
}
