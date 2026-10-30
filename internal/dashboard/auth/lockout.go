package auth

import (
	"strings"
	"sync"
	"time"
)

// lockout rate-limits guesses per bucket (design §3). A bucket's key prefix
// decides its policy:
//
//   - a client ("addr:" one IP address, "node:" one tailnet node) and an
//     operator ("op:", guessable-secret failures from anywhere): 5 failures in
//     15 min lock it for 15 min, doubling to 4 hours;
//   - a network ("net:" an IPv4 /24 or an IPv6 /64): a looser throttle. 50
//     failures in 15 min refuse further guesses from it until the window
//     moves on; it never locks, so it never doubles.
//
// Which buckets a guess counts against depends on what is guessed (lockKeys):
// only the guessable paths reach the network and operator buckets, so one
// address's failures never lock a different address out of SSH or passkeys.
//
// A guess is reserved before it is evaluated (Begin): the reservation counts
// against the bound while the guess is in flight, so a concurrent burst can't
// have more guesses evaluated than the bound allows. Lookups create no state,
// and entries that no longer constrain anything are collected. The operator
// buckets are persisted (persist), so a restart doesn't hand out fresh guesses.
type lockout struct {
	now       func() time.Time
	mu        sync.Mutex
	m         map[string]*lockState
	lastSweep time.Time
	persist   func(map[string]LockRecord) // the operator buckets, after each change; optional
}

type lockState struct {
	fails     []time.Time
	inFlight  int // reserved guesses not yet resolved
	lockUntil time.Time
	lockLen   time.Duration
}

// LockRecord is a persisted operator bucket.
type LockRecord struct {
	Fails     []time.Time   `json:"fails,omitempty"`
	LockUntil time.Time     `json:"lock_until,omitempty"`
	LockLen   time.Duration `json:"lock_len,omitempty"`
}

const (
	loginWindow   = 15 * time.Minute
	loginMaxFails = 5
	loginBaseLock = 15 * time.Minute
	loginMaxLock  = 4 * time.Hour
	netMaxFails   = 50

	// sweepEvery bounds how often a mutation scans for entries to collect.
	sweepEvery = time.Minute
)

func newLockout(now func() time.Time) *lockout {
	if now == nil {
		now = time.Now
	}
	return &lockout{now: now, m: map[string]*lockState{}}
}

// restore loads persisted operator buckets.
func (l *lockout) restore(recs map[string]LockRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, r := range recs {
		if !strings.HasPrefix(k, "op:") {
			continue
		}
		l.m["login:"+k] = &lockState{fails: r.Fails, lockUntil: r.LockUntil, lockLen: r.LockLen}
	}
}

// saveOps hands the operator buckets to persist. Caller holds l.mu.
func (l *lockout) saveOps() {
	if l.persist == nil {
		return
	}
	out := map[string]LockRecord{}
	for k, s := range l.m {
		if op, ok := strings.CutPrefix(k, "login:"); ok && strings.HasPrefix(op, "op:") {
			if len(s.fails) > 0 || s.lockLen > 0 {
				out[op] = LockRecord{Fails: append([]time.Time(nil), s.fails...), LockUntil: s.lockUntil, LockLen: s.lockLen}
			}
		}
	}
	l.persist(out)
}

// throttleOnly reports a bucket that throttles instead of locking.
func throttleOnly(key string) bool { return strings.HasPrefix(key, "net:") }

// maxFails is a bucket's failure bound within loginWindow.
func maxFails(key string) int {
	if throttleOnly(key) {
		return netMaxFails
	}
	return loginMaxFails
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
		if s.inFlight == 0 && len(s.recent(now, loginWindow)) == 0 && now.After(s.lockUntil.Add(loginMaxLock)) {
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
// nothing, when any key is locked or already has its bound of failures and
// in-flight guesses within the window. refused is the key that refused it and
// until when to try again (the end of a lock, when the oldest failure leaves
// the window, or now while guesses are in flight).
func (l *lockout) Begin(keys ...string) (a *attempt, until time.Time, refused string, ok bool) {
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
			return nil, s.lockUntil, k, false
		}
		if f := s.recent(now, loginWindow); len(f)+s.inFlight >= maxFails(k) {
			until := now
			if len(f) > 0 {
				until = f[0].Add(loginWindow)
			}
			return nil, until, k, false
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
	return &attempt{l: l, keys: keys}, time.Time{}, "", true
}

// Fail resolves the guess as wrong: a failure on every key, which may lock it
// (a network bucket only counts it).
func (a *attempt) Fail() {
	a.resolve(func(k string, s *lockState, now time.Time) {
		s.fails = append(s.recent(now, loginWindow), now)
		if !throttleOnly(k) && len(s.fails) >= loginMaxFails {
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
	a.resolve(func(k string, s *lockState, _ time.Time) {
		if !throttleOnly(k) { // a network's count is its neighbours' too
			s.fails, s.lockLen, s.lockUntil = nil, 0, time.Time{}
		}
	})
}

// Release resolves the guess as neither (it was never evaluated).
func (a *attempt) Release() { a.resolve(func(string, *lockState, time.Time) {}) }

func (a *attempt) resolve(f func(string, *lockState, time.Time)) {
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
		f(k, s, now)
	}
	l.sweep(now)
	for _, k := range a.keys {
		if strings.HasPrefix(k, "op:") {
			l.saveOps()
			break
		}
	}
}

// Succeed clears a sign-in key's failure history (a sign-in that needed no
// reserved guess, e.g. a cross-device approval collected by the page).
func (l *lockout) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s, ok := l.m["login:"+key]; ok {
		s.fails, s.lockLen, s.lockUntil = nil, 0, time.Time{}
		if strings.HasPrefix(key, "op:") {
			l.saveOps()
		}
	}
}
