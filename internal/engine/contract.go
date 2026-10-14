package engine

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// stopAsTargetGone turns a plugin's -32011 target_gone answer
// (plugin-contract.md §1.11) into dispatch.ErrTargetClosed, the same
// sentinel a dispatch-detected closure already produces (controller/
// runner.go's StopTarget path) — so a credential mint, a remediation status/
// action call, or a resume that hits target_gone stops the run instead of
// failing it, through the exact machinery that already exists for that.
//
// It is honored ONLY when the answer's data.target names key, the run's own
// trigger target key (finding 11, plugin-contract.md §1.11): a credential
// mint calls the CONNECTOR's own mint verb, not necessarily one addressing
// this run's target, so a target_gone it answers about some OTHER resource
// (a revoked installation's token endpoint naming a different repo, say)
// must never be read as "this run's own target is gone" — connector.
// TargetGoneOrUpstream is the shared gate (every target_gone interpreter in
// the tree uses it). A mismatched or absent target comes back as a loud,
// non-retryable upstream failure instead. Any other error, including a nil
// one, is returned unchanged.
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
