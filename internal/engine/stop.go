package engine

import "fmt"

// StopRunByID cancels a live run by its history id — the operator Stop control
// (dashboard §4.3). It reaches the flow runner's per-run cancel registry
// (flow.Runner.Stop); the run fails mid-step, keeping anything already
// committed and staying retryable from any step. It returns an error only when
// no run by that id is running here (already finished, or an unknown id) so the
// caller can say so.
func (e *Engine) StopRunByID(histID string) error {
	if e.flow == nil {
		return fmt.Errorf("stop: no connectors-model flow runner is configured")
	}
	if !e.flow.Stop(histID) {
		return fmt.Errorf("run %s is not running (it may have already finished)", histID)
	}
	return nil
}

// RunActiveByID reports whether a run with the given history id is live in this
// process — the authority on "still running" the history file lags behind.
func (e *Engine) RunActiveByID(histID string) bool {
	return e.flow != nil && e.flow.Running(histID)
}
