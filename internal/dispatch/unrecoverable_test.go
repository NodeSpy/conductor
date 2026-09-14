package dispatch

import (
	"errors"
	"fmt"
	"testing"
)

func TestUnrecoverableNilPassesThrough(t *testing.T) {
	if got := Unrecoverable(nil); got != nil {
		t.Fatalf("Unrecoverable(nil) = %v, want nil", got)
	}
	if IsUnrecoverable(nil) {
		t.Fatal("IsUnrecoverable(nil) should be false")
	}
}

func TestUnrecoverableRoundTrips(t *testing.T) {
	base := errors.New("workspace create failed")
	wrapped := Unrecoverable(base)

	if !IsUnrecoverable(wrapped) {
		t.Fatal("a directly wrapped error must report unrecoverable")
	}
	if wrapped.Error() != base.Error() {
		t.Errorf("Error() = %q, want %q — the wrapper must not mangle the message", wrapped.Error(), base.Error())
	}
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is must still find the underlying error through the wrapper")
	}
}

func TestUnrecoverableSurvivesFurtherWrapping(t *testing.T) {
	// The call chain (controller → dispatch closure → flow's stepError) wraps
	// further with fmt.Errorf/%w at each hop — IsUnrecoverable must still find
	// the marker no matter how deep.
	wrapped := fmt.Errorf("dispatch step %q: %w", "fix", Unrecoverable(errors.New("boom")))
	if !IsUnrecoverable(wrapped) {
		t.Fatal("IsUnrecoverable must see through further %w wrapping")
	}
}

func TestOrdinaryErrorIsNotUnrecoverable(t *testing.T) {
	if IsUnrecoverable(errors.New("plain step error")) {
		t.Fatal("a plain error must not be classified unrecoverable")
	}
}
