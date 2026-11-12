package engine

import (
	"context"
	"sync"
)

// Slot priorities. A freed slot goes to the oldest high-priority waiter first,
// then the oldest normal one — FIFO within a class.
const (
	prioNormal = iota
	prioHigh
	numPrios
)

// slotPriority ranks a trigger for the concurrency cap. An interactive event
// (its declared priority — someone is waiting on you) goes first; the rest is
// background maintenance that can wait. Without this a burst of fixers — a
// sweep fan-out or a batch `conductor force` — holds every slot and a fresh
// interactive event sits behind all of them.
func slotPriority(interactive bool) int {
	if interactive {
		return prioHigh
	}
	return prioNormal
}

// slots is the concurrent-agent cap: a counting semaphore whose waiters are
// served by priority, then arrival order. A plain buffered channel can't jump
// the queue, which is exactly what a waiting review needs to do.
type slots struct {
	mu      sync.Mutex
	cap     int
	used    int
	waiters [numPrios][]*slotWaiter
}

// slotWaiter is one blocked acquire: closed ch hands it the slot; pos, when
// set, is told how many waiters will be served before it, whenever that
// changes.
type slotWaiter struct {
	ch    chan struct{}
	pos   func(ahead int)
	ahead int
}

func newSlots(cap int) *slots { return &slots{cap: cap} }

// acquire takes a slot, blocking until one is handed over. Returns false if ctx
// is cancelled first (the slot, if handed over in the race, is passed on).
func (s *slots) acquire(ctx context.Context, prio int) bool {
	return s.acquireWatch(ctx, prio, nil)
}

// acquireWatch is acquire, telling pos (when set) how many waiters are ahead
// of this one: once as it starts waiting, then whenever the queue moves.
// pos is called without the lock held and must not block.
func (s *slots) acquireWatch(ctx context.Context, prio int, pos func(ahead int)) bool {
	s.mu.Lock()
	if s.used < s.cap && s.queued() == 0 {
		s.used++
		s.mu.Unlock()
		return true
	}
	w := &slotWaiter{ch: make(chan struct{}), pos: pos, ahead: -1}
	s.waiters[prio] = append(s.waiters[prio], w)
	moved := s.positions()
	s.mu.Unlock()
	tell(moved)

	select {
	case <-w.ch:
		return true
	case <-ctx.Done():
		s.mu.Lock()
		if s.remove(prio, w) {
			moved := s.positions()
			s.mu.Unlock()
			tell(moved)
			return false
		}
		s.mu.Unlock()
		// release handed us the slot between ctx firing and the lock: we own it,
		// so give it to the next waiter rather than leak it.
		s.release()
		return false
	}
}

// positionCall is one pending position report.
type positionCall struct {
	fn    func(int)
	ahead int
}

// positions recomputes each watching waiter's place (s.mu held) and returns
// the reports to make — only those whose place changed.
func (s *slots) positions() []positionCall {
	var out []positionCall
	n := 0
	for p := numPrios - 1; p >= 0; p-- {
		for _, w := range s.waiters[p] {
			if w.pos != nil && w.ahead != n {
				w.ahead = n
				out = append(out, positionCall{w.pos, n})
			}
			n++
		}
	}
	return out
}

func tell(calls []positionCall) {
	for _, c := range calls {
		c.fn(c.ahead)
	}
}

// tryAcquire takes a slot only if one is free and nobody is waiting.
func (s *slots) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used < s.cap && s.queued() == 0 {
		s.used++
		return true
	}
	return false
}

// release frees a slot, handing it straight to the next waiter if any (used
// stays constant across a hand-off).
func (s *slots) release() {
	s.mu.Lock()
	for p := numPrios - 1; p >= 0; p-- {
		if q := s.waiters[p]; len(q) > 0 {
			s.waiters[p] = q[1:]
			close(q[0].ch)
			moved := s.positions()
			s.mu.Unlock()
			tell(moved)
			return
		}
	}
	if s.used > 0 {
		s.used--
	}
	s.mu.Unlock()
}

func (s *slots) queued() int {
	n := 0
	for _, q := range s.waiters {
		n += len(q)
	}
	return n
}

func (s *slots) remove(prio int, w *slotWaiter) bool {
	q := s.waiters[prio]
	for i, c := range q {
		if c == w {
			s.waiters[prio] = append(q[:i:i], q[i+1:]...)
			return true
		}
	}
	return false
}

// full reports whether every slot is taken (a new acquire would wait).
func (s *slots) full() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used >= s.cap
}

// usage is the slots in use and the cap.
func (s *slots) usage() (used, cap int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.cap
}
