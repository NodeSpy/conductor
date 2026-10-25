package flow

import (
	"context"
	"errors"
	"testing"
)

func TestCancelRegistryStop(t *testing.T) {
	r := &Runner{cancelReg: &cancelReg{}}
	ctx, cancel := context.WithCancelCause(context.Background())
	r.cancelReg.register("r-1", cancel)

	if !r.Running("r-1") {
		t.Fatalf("r-1 should be running")
	}
	if r.Running("r-2") {
		t.Fatalf("r-2 should not be running")
	}

	if !r.Stop("r-1") {
		t.Fatalf("Stop(r-1) = false, want true")
	}
	if !errors.Is(context.Cause(ctx), ErrStopped) {
		t.Errorf("cause = %v, want ErrStopped", context.Cause(ctx))
	}

	// A second stop of the same id (already unregistered? no — Stop doesn't
	// unregister; Run's defer does) still reports it was running until the run
	// deregisters.
	r.cancelReg.unregister("r-1")
	if r.Stop("r-1") {
		t.Errorf("Stop after unregister should report false")
	}
	if r.Running("r-1") {
		t.Errorf("r-1 should be gone after unregister")
	}
}

func TestCancelRegistryEmptyID(t *testing.T) {
	r := &Runner{cancelReg: &cancelReg{}}
	_, cancel := context.WithCancelCause(context.Background())
	r.cancelReg.register("", cancel) // no-op
	if r.Running("") {
		t.Errorf("empty id must never register")
	}
}
