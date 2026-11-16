package dispatch

import (
	"context"
	"sync"
	"time"
)

// A foreground dispatch of a run bound to its target is stopped when the
// target closes (StopTarget), as a controller runtime's session is. `paseo
// run` reports the agent's id only when its turn ends, so the agent is named
// at launch by a label only this dispatch knows, and found by it.

// boundLabel names the agent a bound foreground dispatch launched.
const boundLabel = "conductor-bound"

// boundPoll is how often a stopped dispatch looks for its agent while the
// launch is still bringing it up; a var so tests can shorten it.
var boundPoll = time.Second

// boundRun is one such dispatch, in flight.
type boundRun struct {
	key  string
	stop chan struct{} // closed by StopTarget

	mu       sync.Mutex
	stopped  bool
	labels   map[string]string // what finds its agent
	archived map[string]bool
}

func (b *boundRun) isStopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

// find sets the labels its agent is found by.
func (b *boundRun) find(labels map[string]string) {
	b.mu.Lock()
	b.labels = labels
	b.mu.Unlock()
}

// trackBound records a bound foreground dispatch on the target keyed key,
// until untrackBound.
func (d *Dispatcher) trackBound(key, token string) *boundRun {
	b := &boundRun{key: key, stop: make(chan struct{}),
		labels: map[string]string{boundLabel: token}, archived: map[string]bool{}}
	d.boundMu.Lock()
	if d.bound == nil {
		d.bound = map[*boundRun]struct{}{}
	}
	d.bound[b] = struct{}{}
	d.boundMu.Unlock()
	return b
}

func (d *Dispatcher) untrackBound(b *boundRun) {
	d.boundMu.Lock()
	delete(d.bound, b)
	d.boundMu.Unlock()
}

// StopTarget stops every bound foreground dispatch in flight on the target
// keyed key: the target closed, so the work is moot and anything the agent
// does from here lands on a dead target. Each one's agent is archived, with
// the workspace conductor made for it, and its Dispatch returns
// ErrTargetClosed. Returns how many it stopped.
func (d *Dispatcher) StopTarget(_ context.Context, key string) int {
	d.boundMu.Lock()
	var hit []*boundRun
	for b := range d.bound {
		if b.key == key {
			hit = append(hit, b)
		}
	}
	d.boundMu.Unlock()
	n := 0
	for _, b := range hit {
		b.mu.Lock()
		if !b.stopped {
			b.stopped = true
			close(b.stop)
			n++
		}
		b.mu.Unlock()
	}
	return n
}

// stopWhenClosed waits for b to be stopped, then archives its agent as soon
// as there is one and ends the launch (cancelRun). Run in its own goroutine,
// until ctx ends with the dispatch.
func (d *Dispatcher) stopWhenClosed(ctx context.Context, b *boundRun, cancelRun func()) {
	select {
	case <-ctx.Done():
		return
	case <-b.stop:
	}
	for {
		if d.archiveBound(ctx, b) > 0 {
			cancelRun()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(boundPoll):
		}
	}
}

// archiveBound archives the agents of a stopped dispatch: those its labels
// find, and ids. It reports how many it found, archived now or before.
//
// The agents are conductor's own: the label is a token this dispatch minted
// for its launch (or the session token the run recorded for the agent it
// resumes), and ids are what the launch itself reported. They enter the
// ownership ledger here, since the launch that would have recorded them was
// cut short, and are archived through Archive like any other.
func (d *Dispatcher) archiveBound(ctx context.Context, b *boundRun, ids ...string) int {
	b.mu.Lock()
	labels := b.labels
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if agents, err := d.backend().ListAgents(ctx, labels); err == nil {
		for _, a := range agents {
			ids = append(ids, a.ID)
		}
	}
	found := 0
	for _, id := range ids {
		if id == "" {
			continue
		}
		found++
		b.mu.Lock()
		done := b.archived[id]
		b.mu.Unlock()
		if done {
			continue
		}
		d.Owned.AddAgent(id)
		if err := d.Archive(ctx, id); err != nil {
			d.logf("stopping agent %s (its target closed): %v", id, err)
			continue
		}
		b.mu.Lock()
		b.archived[id] = true
		b.mu.Unlock()
	}
	return found
}
