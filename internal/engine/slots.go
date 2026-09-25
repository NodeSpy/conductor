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

// slotPriority ranks a trigger kind for the concurrency cap. A review request is
// someone waiting on you; fixers (merge_conflict, changes_requested,
// failing_checks, …) are background maintenance that can wait. Without this a
// burst of fixers — a sweep fan-out or a batch `conductor force` — holds every
// slot and a fresh review request sits behind all of them.
func slotPriority(kind string) int {
	if kind == "review_requested" {
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
	waiters [numPrios][]chan struct{}
}

func newSlots(cap int) *slots { return &slots{cap: cap} }

// acquire takes a slot, blocking until one is handed over. Returns false if ctx
// is cancelled first (the slot, if handed over in the race, is passed on).
func (s *slots) acquire(ctx context.Context, prio int) bool {
	s.mu.Lock()
	if s.used < s.cap && s.queued() == 0 {
		s.used++
		s.mu.Unlock()
		return true
	}
	ch := make(chan struct{})
	s.waiters[prio] = append(s.waiters[prio], ch)
	s.mu.Unlock()

	select {
	case <-ch:
		return true
	case <-ctx.Done():
		s.mu.Lock()
		if s.remove(prio, ch) {
			s.mu.Unlock()
			return false
		}
		s.mu.Unlock()
		// release handed us the slot between ctx firing and the lock: we own it,
		// so give it to the next waiter rather than leak it.
		s.release()
		return false
	}
}

// release frees a slot, handing it straight to the next waiter if any (used
// stays constant across a hand-off).
func (s *slots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := numPrios - 1; p >= 0; p-- {
		if q := s.waiters[p]; len(q) > 0 {
			s.waiters[p] = q[1:]
			close(q[0])
			return
		}
	}
	if s.used > 0 {
		s.used--
	}
}

func (s *slots) queued() int {
	n := 0
	for _, q := range s.waiters {
		n += len(q)
	}
	return n
}

func (s *slots) remove(prio int, ch chan struct{}) bool {
	q := s.waiters[prio]
	for i, c := range q {
		if c == ch {
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
