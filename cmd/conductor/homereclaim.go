package main

import (
	"sync/atomic"

	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// homeRunLive answers the agent sandbox's "is this run still live?" once the
// store is open (wireHomeReclaim). Unset: every run is — no indexed
// per-dispatch home is reclaimed before the daemon can tell.
var homeRunLive atomic.Pointer[func(run string) bool]

// sandboxRunLive is the resolver's RunLive.
func sandboxRunLive(run string) bool {
	f := homeRunLive.Load()
	return f == nil || (*f)(run)
}

// runLive reports whether the run recorded under history id run can still
// use what was kept for its dispatches: it is running, or a restart
// interrupted it and its run record is still there to resume.
func runLive(st *store.Store, run string) bool {
	rec, ok := st.GetHistory(run)
	if !ok {
		return false
	}
	switch rec.Status {
	case "running":
		return true
	case "interrupted":
		for _, p := range st.PendingRuns() {
			if p.ID == rec.RunID {
				return true
			}
		}
	}
	return false
}

// wireHomeReclaim reclaims the agent sandbox's per-dispatch homes when their
// run ends, and sweeps the orphans a previous daemon left — now, at start.
// Returns the unwiring.
func wireHomeReclaim(runner *flow.Runner, st *store.Store) func() {
	r := sandboxResolver.Load()
	if r == nil || st == nil {
		return func() {}
	}
	live := func(run string) bool { return runLive(st, run) }
	homeRunLive.Store(&live)
	if runner != nil {
		runner.RunEnded = func(id string) { go r.ReclaimRun(id) }
	}
	go r.SweepDispatchHomes()
	return func() {
		if runner != nil {
			runner.RunEnded = nil
		}
		homeRunLive.Store(nil)
	}
}
