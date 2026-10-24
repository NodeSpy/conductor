package browser

import (
	"io"
	"testing"
	"time"
)

// An aborted transfer's drain resets its grace on every frame, so a shim still
// streaming is drained in full. A shim that NEVER goes quiet (buggy, slow to
// stop, or hostile) would reset the grace forever and hold fileMu — stalling
// every later transfer — so quiesceFile must also honor an absolute cap
// independent of the per-frame reset. Regression guard for #2.
func TestMuxQuiesceFileAbsoluteCap(t *testing.T) {
	m := &Mux{w: io.Discard, done: make(chan struct{}), drainMax: 60 * time.Millisecond}
	c := &muxChan{file: make(chan fileMsg)}

	// A chatty shim: a frame far faster than fileDrainGrace, forever.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case c.file <- fileMsg{p: []byte("x")}:
			case <-stop:
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() { m.quiesceFile(c); close(done) }()

	select {
	case <-done:
		// Returned under the absolute cap despite the unending stream.
	case <-time.After(2 * time.Second):
		t.Fatal("quiesceFile never returned under a chatty shim — the per-frame grace reset has no absolute cap, so fileMu is held indefinitely")
	}
}
