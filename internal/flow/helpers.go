package flow

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
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
func (r *Runner) execHelper(ctx context.Context, t core.Trigger, step config.Step, id string, shadow bool) (map[string]any, string, error) {
	switch step.HelperForm() {
	case config.HelperSleep:
		return r.execSleep(ctx, t, step, shadow)
	}
	// Unreachable through the loader (config.HelperForm and this switch list
	// the same set) — it fires only for a helper added to the config side and
	// not to this one, which is a build-time mistake worth naming out loud.
	return nil, "", fmt.Errorf("step %q: helper %q has no runner", id, step.HelperForm())
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
