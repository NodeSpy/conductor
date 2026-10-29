package auth

import (
	"sync"
	"time"
)

// lockout rate-limits sign-in and enrollment-redeem attempts per client and per
// operator, with an exponential lock that doubles to a cap (design §6.3).
//
//   - sign-in: 5 failed attempts per 15 min → a 15 min lock, doubling to a cap.
//   - enroll redeem: 3 per hour.
//
// A guess is reserved before it is evaluated (Begin): the reservation counts
// against the bound while the guess is in flight, so a concurrent burst can't
// have more guesses evaluated than the bound allows. Lookups create no state,
// and entries that no longer constrain anything are collected.
type lockout struct {
	now       func() time.Time
	mu        sync.Mutex
	m         map[string]*lockState
	lastSweep time.Time
}

type lockState struct {
	fails     []time.Time
	inFlight  int // reserved guesses not yet resolved
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

	// sweepEvery bounds how often a mutation scans for entries to collect.
	sweepEvery = time.Minute
)

func newLockout(now func() time.Time) *lockout {
	if now == nil {
		now = time.Now
	}
	return &lockout{now: now, m: map[string]*lockState{}}
}

// recent drops failures older than window and returns what is left.
func (s *lockState) recent(now time.Time, window time.Duration) []time.Time {
	cut := now.Add(-window)
	kept := s.fails[:0]
	for _, t := range s.fails {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.fails = kept
	return kept
}

// sweep collects entries that constrain nothing any more: no guess in flight,
// no recent failure, and a lock (if any) long enough over that its doubling
// memory has lapsed. Caller holds l.mu.
func (l *lockout) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepEvery {
		return
	}
	l.lastSweep = now
	for k, s := range l.m {
		if s.inFlight == 0 && len(s.recent(now, redeemWindow)) == 0 && now.After(s.lockUntil.Add(loginMaxLock)) {
			delete(l.m, k)
		}
	}
}

// Locked reports whether a sign-in key is currently locked, and until when. It
// creates no state.
func (l *lockout) Locked(key string) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.m["login:"+key]
	if ok && l.now().Before(s.lockUntil) {
		return true, s.lockUntil
	}
	return false, time.Time{}
}

// attempt is one guess reserved against a set of sign-in keys.
type attempt struct {
	l    *lockout
	keys []string
	done bool
}

// Begin reserves one guess on every key, atomically: it is refused, reserving
// nothing, when any key is locked or already has loginMaxFails failures and
// in-flight guesses within the window. until is when to try again (the end of
// a lock, or now while guesses are in flight).
func (l *lockout) Begin(keys ...string) (a *attempt, until time.Time, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	for _, k := range keys {
		s, found := l.m["login:"+k]
		if !found {
			continue
		}
		if now.Before(s.lockUntil) {
			return nil, s.lockUntil, false
		}
		if len(s.recent(now, loginWindow))+s.inFlight >= loginMaxFails {
			return nil, now, false
		}
	}
	for _, k := range keys {
		s, found := l.m["login:"+k]
		if !found {
			s = &lockState{}
			l.m["login:"+k] = s
		}
		s.inFlight++
	}
	return &attempt{l: l, keys: keys}, time.Time{}, true
}

// Fail resolves the guess as wrong: a failure on every key, which may lock it.
func (a *attempt) Fail() {
	a.resolve(func(s *lockState, now time.Time) {
		s.fails = append(s.recent(now, loginWindow), now)
		if len(s.fails) >= loginMaxFails {
			if s.lockLen == 0 {
				s.lockLen = loginBaseLock
			} else {
				s.lockLen = min(s.lockLen*2, loginMaxLock)
			}
			s.lockUntil = now.Add(s.lockLen)
			s.fails = nil
		}
	})
}

// Succeed resolves the guess as right: every key's failure history is cleared.
func (a *attempt) Succeed() {
	a.resolve(func(s *lockState, _ time.Time) {
		s.fails, s.lockLen, s.lockUntil = nil, 0, time.Time{}
	})
}

// Release resolves the guess as neither (it was never evaluated).
func (a *attempt) Release() { a.resolve(func(*lockState, time.Time) {}) }

func (a *attempt) resolve(f func(*lockState, time.Time)) {
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	now := l.now()
	for _, k := range a.keys {
		s, ok := l.m["login:"+k]
		if !ok {
			continue
		}
		s.inFlight--
		f(s, now)
	}
	l.sweep(now)
}

// Succeed clears a sign-in key's failure history (a sign-in that needed no
// reserved guess, e.g. a cross-device approval collected by the page).
func (l *lockout) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.m["login:"+key]; ok {
		s.fails, s.lockLen, s.lockUntil = nil, 0, time.Time{}
	}
}

// RedeemAllowed reports whether another enroll-redeem is permitted for a client
// (3 per hour), recording the attempt when it is.
func (l *lockout) RedeemAllowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	s, ok := l.m["redeem:"+key]
	if !ok {
		s = &lockState{}
		l.m["redeem:"+key] = s
	}
	if len(s.recent(now, redeemWindow)) >= redeemMaxTries {
		return false
	}
	s.fails = append(s.fails, now)
	return true
}
