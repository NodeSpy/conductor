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
// Any other error, including a nil one, is returned unchanged.
func stopAsTargetGone(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := connector.AsContractError(err); ok && ce.IsTargetGone() {
		return fmt.Errorf("%w: %w", dispatch.ErrTargetClosed, err)
	}
	return err
}
