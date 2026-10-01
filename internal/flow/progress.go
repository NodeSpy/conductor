package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Run progress (connector/progress.go): the signal a run leaves on the thing
// it is handling. The runner owns WHEN — started before the first hook or
// step (so before any worktree is provisioned or agent launched: a run that
// dies at dispatch has still visibly been picked up), finished on every way
// a run can end — and the event's connector owns WHAT. The trigger's
// `options.progress` rides through to the connector undecoded; it is that
// connector's knob.

// runProgress is one run's progress handle. Finishing is once-only, and a
// nil handle (nothing to show) finishes as a no-op.
type runProgress struct {
	p    connector.Progress
	done bool
}

// startProgress starts progress for a run on the connector that emitted its
// event. Shadow and dry runs show nothing: they post nothing anywhere else
// either.
func (r *Runner) startProgress(ctx context.Context, t core.Trigger, spec config.TriggerSpec, batch *Batch, shadow bool) *runProgress {
	if shadow || r.Conns == nil || t.Instance == "" {
		return &runProgress{}
	}
	in, ok := r.Conns.Get(t.Instance)
	if !ok {
		return &runProgress{}
	}
	opts, _ := spec.Options["progress"].(map[string]any)
	run := connector.ProgressRun{Trigger: t, Options: opts}
	if batch != nil {
		run.Batch = batch.Events
	}
	return &runProgress{p: in.StartProgress(ctx, run)}
}

// finish records the outcome once. A run cut short by shutdown records
// nothing: it resumes on restart and finishes then.
func (rp *runProgress) finish(ctx context.Context, o connector.RunOutcome) {
	if rp == nil || rp.p == nil || rp.done {
		return
	}
	rp.done = true
	if ctx.Err() != nil {
		return
	}
	rp.p.Finish(ctx, o)
}

// failedOutcome classifies a run failure into the short, public-safe reason
// progress shows. It never carries the error text itself — that can name
// paths, hosts, or a provider's response, and progress is shown on the
// target for anyone to read.
func failedOutcome(err error) connector.RunOutcome {
	reason := "the run failed"
	var np *noProgressError
	switch {
	case dispatch.IsUnrecoverable(err):
		reason = "the agent couldn't be started"
	case errors.As(err, &np):
		reason = "the change was never pushed"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timed out"
	default:
		if id := failedStepID(err); id != "" {
			reason = fmt.Sprintf("step %q failed", id)
		}
	}
	return connector.RunOutcome{Result: connector.OutcomeFailed, Reason: reason}
}

// ReportOutcome shows a terminal outcome for an event that never reached a
// run — the engine parking a (PR, kind, head) that kept failing. It starts
// and finishes progress in one go, so the subject still says it was seen and
// what became of it.
func (r *Runner) ReportOutcome(ctx context.Context, t core.Trigger, flowRef string, shadow bool, o connector.RunOutcome) {
	spec, _, ok := r.SpecFor(flowRef)
	if !ok {
		return
	}
	shadow = shadow || r.DryRun || (spec.Shadow != nil && *spec.Shadow)
	r.startProgress(ctx, t, spec, nil, shadow).finish(ctx, o)
}
