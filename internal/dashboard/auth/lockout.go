package auth

import (
	"sync"
	"time"
)

// lockout rate-limits sign-in and enrollment-redeem attempts per client, with an
// exponential lock that doubles to a cap (design §6.3).
//
//   - sign-in: 5 failed assertions per 15 min → a 15 min lock, doubling to a cap.
//   - enroll redeem: 3 per hour.
type lockout struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*lockState
}

type lockState struct {
	fails     []time.Time
	lockUntil time.Time
	lockLen   time.Duration
}

const (
	loginWindow   = 15 * time.Minute
	loginMaxFails = 5
	loginBaseLock = 15 * time.Minute
	loginMaxLock  = 4 * time.Hour

	redeemWindow   = time.Hour
	redeemMaxTries = 3
)

func newLockout(now func() time.Time) *lockout {
	if now == nil {
		now = time.Now
	}
	return &lockout{now: now, m: map[string]*lockState{}}
}

func (l *lockout) state(key string) *lockState {
	s, ok := l.m[key]
	if !ok {
		s = &lockState{}
		l.m[key] = s
	}
	return s
}

// Locked reports whether a sign-in key is currently locked, and until when.
func (l *lockout) Locked(key string) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state("login:" + key)
	now := l.now()
	if now.Before(s.lockUntil) {
		return true, s.lockUntil
	}
	return false, time.Time{}
}

// Fail records a failed sign-in and may extend the lock.
func (l *lockout) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state("login:" + key)
	now := l.now()
	cut := now.Add(-loginWindow)
	kept := s.fails[:0]
	for _, t := range s.fails {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.fails = append(kept, now)
	if len(s.fails) >= loginMaxFails {
		if s.lockLen == 0 {
			s.lockLen = loginBaseLock
		} else {
			s.lockLen *= 2
			if s.lockLen > loginMaxLock {
				s.lockLen = loginMaxLock
			}
		}
		s.lockUntil = now.Add(s.lockLen)
		s.fails = nil
	}
}

// Succeed clears a sign-in key's failure history on success.
func (l *lockout) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, "login:"+key)
}

// RedeemAllowed reports whether another enroll-redeem is permitted for a client
// (3 per hour), recording the attempt when it is.
func (l *lockout) RedeemAllowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state("redeem:" + key)
	now := l.now()
	cut := now.Add(-redeemWindow)
	kept := s.fails[:0]
	for _, t := range s.fails {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.fails = kept
	if len(s.fails) >= redeemMaxTries {
		return false
	}
	s.fails = append(s.fails, now)
	return true
}
