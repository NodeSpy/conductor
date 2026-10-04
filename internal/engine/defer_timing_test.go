package engine

import (
	"testing"
	"time"
)

// setDeferTimings shortens the deferral timing knobs for one test, under the
// lock a timer from an earlier test may still be reading them through.
func setDeferTimings(t *testing.T, minWait time.Duration, backoff []time.Duration) {
	t.Helper()
	deferTimingMu.Lock()
	oldMin, oldBackoff := minDeferredWait, resumeRecheckBackoff
	minDeferredWait = minWait
	if backoff != nil {
		resumeRecheckBackoff = backoff
	}
	deferTimingMu.Unlock()
	t.Cleanup(func() {
		deferTimingMu.Lock()
		minDeferredWait, resumeRecheckBackoff = oldMin, oldBackoff
		deferTimingMu.Unlock()
	})
}
