package dispatch

import "errors"

// UnrecoverableError marks a dispatch failure that happened before, or while,
// establishing a working runtime session: the resolved runtime/controller is
// unknown or its transport isn't runnable in this build, the conductor-supplied
// worktree/workspace never came up, or the dispatched agent's session crashed
// before it could do any work.
//
// This is a different class from an ORDINARY step failure — a gate discard, a
// command's own non-zero exit, a connector verb erroring — every one of which
// happened AFTER a runtime was actually reached, so the step DID something. An
// unrecoverable dispatch failure means retrying the identical step is exactly
// as futile the second time; it needs an operator, not a re-run. The flow
// runner (internal/flow) escalates these instead of recording an ordinary
// workflow_failed (#60).
type UnrecoverableError struct {
	err error
}

func (e *UnrecoverableError) Error() string { return e.err.Error() }
func (e *UnrecoverableError) Unwrap() error { return e.err }

// Unrecoverable wraps err as an unrecoverable dispatch failure. Returns nil
// when err is nil, so a call site can wrap unconditionally.
func Unrecoverable(err error) error {
	if err == nil {
		return nil
	}
	return &UnrecoverableError{err: err}
}

// IsUnrecoverable reports whether err (or anything it wraps) is an
// unrecoverable dispatch failure.
func IsUnrecoverable(err error) bool {
	var u *UnrecoverableError
	return errors.As(err, &u)
}
