package flow

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// stopAsTargetGone turns a plugin's -32011 target_gone answer
// (plugin-contract.md §1.11) into dispatch.ErrTargetClosed: the run is
// STOPPED (stop hooks, no failure), exactly like the dispatch-detected
// closure a running agent's turn already produces. Every place that already
// treats a dispatch-side closure as a stop (runSteps' stop/fail hook switch,
// execWithRetry's retry-loop break, the workflow-level finish in Run) then
// treats a plugin's own detection of it identically, with no further
// changes needed there. Any other error, including a nil one, is returned
// unchanged.
func stopAsTargetGone(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := connector.AsContractError(err); ok && ce.IsTargetGone() {
		return fmt.Errorf("%w: %w", dispatch.ErrTargetClosed, err)
	}
	return err
}

// noStepRetry reports whether err is a plugin contract error a step's
// retry: must never retry (plugin-contract.md §1.11):
//
//   - -32012 invalid never retries — the request can never succeed.
//   - -32010 upstream retries only when the plugin marked it retryable; an
//     upstream answer that says it is NOT retryable must not be retried
//     just because the step configured a retry: block.
//
// rate_limited and not_ready are not decided here: they are retried
// independently of retry: (RetryContract, applied at the invoke itself,
// before a step-level attempt even completes), so by the time an error
// reaches this check they have already exhausted their own bounded retry.
func noStepRetry(err error) bool {
	ce, ok := connector.AsContractError(err)
	if !ok {
		return false
	}
	if ce.IsInvalid() {
		return true
	}
	if ce.IsUpstream() && !ce.UpstreamRetryable() {
		return true
	}
	return false
}

// redactedErr is returned at a boundary where only a redacted string may
// cross (an agent-visible skill-verb reply): its Error() is the caller's own
// (already redacted) text, never err's raw one, but Unwrap keeps err
// reachable so errors.As/Is still finds a *connector.ContractError
// underneath — redacting the TEXT must not cost the TYPE.
type redactedErr struct {
	msg string
	err error
}

func (e *redactedErr) Error() string { return e.msg }
func (e *redactedErr) Unwrap() error { return e.err }
