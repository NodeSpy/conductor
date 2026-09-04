package flow

import (
	"context"
	"sync"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// Clock abstracts time for the grouper so tests drive debounce windows with a
// fake clock instead of sleeping.
type Clock interface {
	Now() time.Time
	// AfterFunc schedules f after d and returns a stoppable/resettable timer.
	AfterFunc(d time.Duration, f func()) GroupTimer
}

// GroupTimer is the timer surface the grouper needs.
type GroupTimer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// realClock is the production Clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) GroupTimer {
	return time.AfterFunc(d, f)
}

// DefaultWindow is the debounce window when group: sets none.
const DefaultWindow = 15 * time.Second

// DefaultMaxWaitFactor caps how long a never-quiet group defers: max_wait
// defaults to 4× the window.
const DefaultMaxWaitFactor = 4

// Grouper batches a trigger's events by key with a debounce window: the
// window resets on each new event and the batch fires once the group goes
// quiet (capped by max_wait). At most one run per key is in flight — events
// arriving during a run form the next batch. Ungrouped triggers (no group:,
// or a key that resolves per-event) pass through immediately.
type Grouper struct {
	clock Clock
	fire  func(key string, events []core.Trigger)

	mu     sync.Mutex
	groups map[string]*groupState
}

type groupState struct {
	pending  []core.Trigger
	timer    GroupTimer
	first    time.Time // when the current batch started collecting
	inFlight bool
}

// NewGrouper builds a Grouper firing batches through fire (called on its own
// goroutine). clock nil = real time.
func NewGrouper(clock Clock, fire func(key string, events []core.Trigger)) *Grouper {
	if clock == nil {
		clock = realClock{}
	}
	return &Grouper{clock: clock, fire: fire, groups: map[string]*groupState{}}
}

// Add routes one event into its group. key is the fully-resolved group key
// (trigger identity + rendered group key); window/maxWait come from the
// trigger's group: spec (zero = defaults).
func (g *Grouper) Add(key string, t core.Trigger, window, maxWait time.Duration) {
	if window <= 0 {
		window = DefaultWindow
	}
	if maxWait <= 0 {
		maxWait = DefaultMaxWaitFactor * window
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.groups[key]
	if !ok {
		st = &groupState{}
		g.groups[key] = st
	}
	if len(st.pending) == 0 {
		st.first = g.clock.Now()
	}
	st.pending = append(st.pending, t)

	if st.inFlight {
		// A run for this key is still going; the pending list is the next
		// batch — it fires when the run completes (see done).
		return
	}
	// Debounce: reset the window on each event, but never past max_wait from
	// the batch's first event.
	wait := window
	if elapsed := g.clock.Now().Sub(st.first); elapsed+wait > maxWait {
		wait = maxWait - elapsed
		if wait < 0 {
			wait = 0
		}
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = g.clock.AfterFunc(wait, func() { g.flush(key, window, maxWait) })
}

// flush fires the pending batch for a key (if any and no run is in flight).
func (g *Grouper) flush(key string, window, maxWait time.Duration) {
	g.mu.Lock()
	st, ok := g.groups[key]
	if !ok || st.inFlight || len(st.pending) == 0 {
		g.mu.Unlock()
		return
	}
	batch := st.pending
	st.pending = nil
	st.inFlight = true
	st.timer = nil
	g.mu.Unlock()

	go func() {
		g.fire(key, batch)
		g.done(key, window, maxWait)
	}()
}

// done marks a key's run finished; a batch that accumulated during the run
// starts a fresh debounce cycle.
func (g *Grouper) done(key string, window, maxWait time.Duration) {
	g.mu.Lock()
	st, ok := g.groups[key]
	if !ok {
		g.mu.Unlock()
		return
	}
	st.inFlight = false
	if len(st.pending) == 0 {
		delete(g.groups, key)
		g.mu.Unlock()
		return
	}
	st.first = g.clock.Now()
	st.timer = g.clock.AfterFunc(window, func() { g.flush(key, window, maxWait) })
	g.mu.Unlock()
}

// GroupKey renders a trigger's group key. An empty key expression defaults to
// the event's own identity (dedup signature, else the trigger key + title) —
// each event is its own run.
func GroupKey(spec string, t core.Trigger, data map[string]any) (string, error) {
	if spec == "" {
		if t.Dedup != "" {
			return t.Dedup, nil
		}
		return t.Key() + ":" + t.Title, nil
	}
	return render(spec, data)
}

// Wait is a test helper: it blocks until no group state remains or ctx ends.
func (g *Grouper) Wait(ctx context.Context) {
	for {
		g.mu.Lock()
		n := len(g.groups)
		g.mu.Unlock()
		if n == 0 || ctx.Err() != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
