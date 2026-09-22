// Package debounce coalesces rapid per-key events into a single fire once a key
// has been quiet for its window. It is the settle logic shared by conductor's
// local-watch sources (fswatch, logwatch): one download's burst of filesystem
// events, or one error's burst of log lines, collapses into a single trigger
// carrying the last event's payload.
package debounce

import (
	"sync"
	"time"
)

// Timer is the subset of *time.Timer the debouncer needs; a fake stands in for
// deterministic tests.
type Timer interface {
	Reset(time.Duration) bool
	Stop() bool
}

// NewFunc constructs a Timer that calls f after d. Real is the production one.
type NewFunc func(d time.Duration, f func()) Timer

// Real builds a live timer (time.AfterFunc).
func Real(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Debouncer coalesces Arm(key, payload) calls per integer key. Rapid arms for
// the same key restart that key's quiet-window timer; when the key finally goes
// quiet for its window, fire(key, lastPayload) runs exactly once.
type Debouncer[T any] struct {
	mu     sync.Mutex
	state  map[int]*entry[T]
	newFn  NewFunc
	window func(key int) time.Duration
	fire   func(key int, payload T)
}

type entry[T any] struct {
	timer   Timer
	payload T
}

// New builds a Debouncer. newFn may be nil (defaults to Real). window returns
// the quiet period for a key; fire is invoked once per settled burst.
func New[T any](newFn NewFunc, window func(key int) time.Duration, fire func(key int, payload T)) *Debouncer[T] {
	if newFn == nil {
		newFn = Real
	}
	return &Debouncer[T]{state: map[int]*entry[T]{}, newFn: newFn, window: window, fire: fire}
}

// Arm records payload for key and (re)starts its quiet-window timer.
func (d *Debouncer[T]) Arm(key int, payload T) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.state[key]
	if e == nil {
		e = &entry[T]{}
		d.state[key] = e
	}
	e.payload = payload
	if e.timer == nil {
		e.timer = d.newFn(d.window(key), func() { d.onFire(key) })
	} else {
		e.timer.Reset(d.window(key))
	}
}

func (d *Debouncer[T]) onFire(key int) {
	d.mu.Lock()
	var p T
	if e := d.state[key]; e != nil {
		p = e.payload
	}
	d.mu.Unlock()
	d.fire(key, p)
}
