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
// changes needed there.
//
// It is honored ONLY when the answer's data.target names key, the run's own
// trigger target key (finding 11): a flow step may call ANY connector's verb
// for ANY target (a chat connector's `post` to a notification channel, say),
// and that call's own target_gone — the channel was deleted — must never be
// read as "this run's own target (the PR this workflow is about) is gone."
// Without this, a connector's own missing notification target could silently
// stop an unrelated PR run. connector.TargetGoneOrUpstream is the shared gate
// (every target_gone
// interpreter in the tree uses it). A mismatched or absent target comes back
// as a loud, non-retryable upstream failure instead — never silently
// dropped, and execWithRetry's own non-retryable-upstream check already
// refuses to retry it regardless of the step's retry:. Any other error,
// including a nil one, is returned unchanged.
func stopAsTargetGone(err error, key string) error {
	if err == nil {
		return nil
	}
	result, isStop := connector.TargetGoneOrUpstream(err, key)
	if isStop {
		return fmt.Errorf("%w: %w", dispatch.ErrTargetClosed, result)
	}
	return result
}

// noStepRetry reports whether err is a plugin contract error a step's
// retry: must never retry (plugin-contract.md §1.11):
//
//   - -32012 invalid never retries — the request can never succeed.
//   - -32010 upstream retries only when the plugin marked it retryable; an
//     upstream answer that says it is NOT retryable must not be retried
//     just because the step configured a retry: block.
//   - -32013 rate_limited and -32014 not_ready are retried independently of
//     retry: by RetryContract, INSIDE the invoke itself, before a
//     step-level attempt even completes — so by the time either reaches
//     this check, the contract layer has already spent its own bounded
//     retry (and budget, §1.11) on it. Retrying it again here would
//     compound the waits: the step's own attempt would re-enter the same
//     call, which re-runs RetryContract's retry from scratch. The contract
//     layer owns these two codes; the step-level retry excludes them
//     entirely, succeed or exhausted.
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
	if ce.IsRateLimited() || ce.IsNotReady() {
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
