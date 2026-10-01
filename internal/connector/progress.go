package connector

import (
	"context"

	"github.com/NodeSpy/conductor/internal/core"
)

// Run progress is the live "conductor has this" signal a run leaves on the
// thing it is handling, where its source has somewhere to put one. The flow
// runner drives the lifecycle — a run starting, and how it ended — and the
// connector that produced the event decides what that looks like on its side
// (GitHub: a reaction on the comment or review, a commit status on the PR).
// Nothing about either face leaks into the engine: it hands over the trigger
// and an outcome, and never learns whether anything was posted.
//
// Reporting is best-effort by contract. An implementation logs and audits its
// own failures and never returns one: a run is never failed, delayed past a
// short bound, or blocked by its progress signal.

// Run outcomes a ProgressRun can finish with.
const (
	// OutcomeOK: every step succeeded.
	OutcomeOK = "ok"
	// OutcomeFailed: the run failed, gave up, escalated, or was parked.
	OutcomeFailed = "failed"
	// OutcomeStopped: the run was stopped because its target went away (a
	// PR closed under a fixer) — moot, not failed.
	OutcomeStopped = "stopped"
)

// RunOutcome is how a run ended.
type RunOutcome struct {
	Result string // OutcomeOK | OutcomeFailed | OutcomeStopped
	// Reason is a short, public-safe phrase for a failure ("the agent
	// couldn't start"). It may be shown on the target, so it never carries
	// an error string, a path, or anything else from the run's internals.
	Reason string
}

// ProgressRun is the run a reporter is asked to show.
type ProgressRun struct {
	Trigger core.Trigger
	// Batch is every event a grouped run covers (nil for a single event); a
	// reporter shows progress on each one's subject.
	Batch []core.Trigger
	// Options is the trigger's `progress:` option (nil when unset) — the
	// per-trigger knob, decoded by the connector that declares it.
	Options map[string]any
}

// ProgressReporter is the optional face of a connector whose events can show
// a run's progress.
type ProgressReporter interface {
	// StartProgress marks the run as taken and returns the handle that
	// finishes it, or nil when there is nothing to show (not this
	// connector's kind of event, reporting off for the trigger, no target).
	// It may make a few bounded API calls; it never blocks indefinitely.
	StartProgress(ctx context.Context, run ProgressRun) Progress
}

// Progress is one run's in-flight progress signal.
type Progress interface {
	// Finish records the outcome. Called at most once.
	Finish(ctx context.Context, o RunOutcome)
}

// StartProgress starts run progress on this instance's reporter. nil when
// the connector has no progress face or is disabled.
func (in *Instance) StartProgress(ctx context.Context, run ProgressRun) Progress {
	if in == nil || !in.Enabled {
		return nil
	}
	pr, ok := in.Impl.(ProgressReporter)
	if !ok {
		return nil
	}
	return pr.StartProgress(ctx, run)
}
