package flow

import (
	"context"
	"errors"
	"sync"
)

// Operator-driven stop (dashboard §4.3 "Stop run"): an in-flight run is a
// goroutine driving a context; stopping it means cancelling that context with
// a cause the rest of the run can tell apart from a daemon shutdown. The run
// then fails mid-step — anything already committed (a pushed branch, a posted
// comment) stays, exactly as a crash would leave it — and its signed record
// stays retryable from any step.
//
// The registry is keyed by the HISTORY id (what the dashboard and
// `conductor runs` show), populated when a run's record is created and cleared
// when it ends. A shadow/dry run has no record id and so is never registered.

// ErrStopped is the cancellation cause an operator stop carries. shuttingDown
// stays false for it (the stored shutdown parent is a different context), so
// the run is recorded as a failure, not an interruption that resumes on boot.
var ErrStopped = errors.New("stopped by operator")

type cancelReg struct {
	mu      sync.Mutex
	cancels map[string]context.CancelCauseFunc
}

func (c *cancelReg) register(id string, cancel context.CancelCauseFunc) {
	if c == nil || id == "" {
		return
	}
	c.mu.Lock()
	if c.cancels == nil {
		c.cancels = map[string]context.CancelCauseFunc{}
	}
	c.cancels[id] = cancel
	c.mu.Unlock()
}

func (c *cancelReg) unregister(id string) {
	if c == nil || id == "" {
		return
	}
	c.mu.Lock()
	delete(c.cancels, id)
	c.mu.Unlock()
}

// Stop cancels the live run with the given history id. It reports whether a
// run by that id was running (false = already finished, never started here, or
// an unknown id). The cancellation is a request: the step gets ctx-cancel and
// unwinds at its next cancellation point, checkpointing as it goes.
func (r *Runner) Stop(id string) bool {
	if r.cancelReg == nil {
		return false
	}
	r.cancelReg.mu.Lock()
	cancel, ok := r.cancelReg.cancels[id]
	r.cancelReg.mu.Unlock()
	if !ok {
		return false
	}
	cancel(ErrStopped)
	return true
}

// Running reports whether a run with the given history id is live in this
// process (its cancel is registered). The dashboard uses it to tell a
// still-running record from a finished one without racing the history file.
func (r *Runner) Running(id string) bool {
	if r.cancelReg == nil {
		return false
	}
	r.cancelReg.mu.Lock()
	_, ok := r.cancelReg.cancels[id]
	r.cancelReg.mu.Unlock()
	return ok
}
