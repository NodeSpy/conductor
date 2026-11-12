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
		// A run waiting for its runtime is not running, but it is not over
		// either: stopping it drops it, as if its target had gone.
		if e.stopWaiting(histID) {
			return nil
		}
		return fmt.Errorf("run %s is not running (it may have already finished)", histID)
	}
	return nil
}

// stopWaiting drops the run whose waiting execution is histID, reporting
// whether there was one.
func (e *Engine) stopWaiting(histID string) bool {
	e.infra.mu.Lock()
	var hit *infraWaiter
	for _, w := range e.infra.runs {
		if !w.claimed && w.run.InfraWait != nil && w.run.InfraWait.History == histID {
			hit = w
			break
		}
	}
	e.infra.mu.Unlock()
	if hit == nil {
		return false
	}
	e.dropWaiter(e.infraCtx(), hit, "stopped by an operator")
	return true
}

// RunActiveByID reports whether a run with the given history id is live in this
// process — the authority on "still running" the history file lags behind.
func (e *Engine) RunActiveByID(histID string) bool {
	return e.flow != nil && e.flow.Running(histID)
}
