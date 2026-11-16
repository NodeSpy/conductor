package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// A run bound to its target is cancelled with cause dispatch.ErrTargetClosed
// when the target closes (the engine's bindRunToTarget). From then on the run
// is stopped, not failed: no further step, push or hook runs except the
// `at: stop` hooks.

// targetClosed reports whether ctx's run was stopped because its target
// closed.
func targetClosed(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), dispatch.ErrTargetClosed)
}

// asTargetClosed tags err as a stop when ctx's run was stopped because its
// target closed: whatever the step returned (a cancelled agent wait, a verb
// cut off mid-call) is the stop's doing, not a failure.
func asTargetClosed(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, dispatch.ErrTargetClosed) || !targetClosed(ctx) {
		return err
	}
	return fmt.Errorf("%w (%v)", dispatch.ErrTargetClosed, err)
}

// ErrPushTargetClosed is the push gate's refusal: the dispatch's target
// closed after its work began, so conductor pushes nothing for it.
var ErrPushTargetClosed = fmt.Errorf("%w: conductor does not push for it", dispatch.ErrTargetClosed)

// PushRefusal is the one gate every conductor push of an agent's work passes
// (the git.push verb and the end-of-step push): ErrPushTargetClosed when the
// dispatch's target closed after its work began, else nil.
func (r *Runner) PushRefusal(dispatchID string) error {
	if r == nil || r.TargetClosed == nil || !r.TargetClosed(dispatchID) {
		return nil
	}
	return ErrPushTargetClosed
}
