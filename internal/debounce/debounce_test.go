package debounce

import (
	"sync"
	"testing"
	"time"
)

type fakeTimer struct {
	mu     sync.Mutex
	f      func()
	resets int
}

func (t *fakeTimer) Reset(time.Duration) bool {
	t.mu.Lock()
	t.resets++
	t.mu.Unlock()
	return true
}
func (t *fakeTimer) Stop() bool { return true }
func (t *fakeTimer) fire()      { t.f() }

func TestArmCollapsesBurst(t *testing.T) {
	var ft *fakeTimer
	nt := func(_ time.Duration, f func()) Timer {
		ft = &fakeTimer{f: f}
		return ft
	}
	var mu sync.Mutex
	var fired []string
	d := New(nt,
		func(int) time.Duration { return time.Second },
		func(_ int, payload string) { mu.Lock(); fired = append(fired, payload); mu.Unlock() },
	)

	d.Arm(0, "a") // creates the timer
	d.Arm(0, "b") // resets
	d.Arm(0, "c") // resets

	if len(fired) != 0 {
		t.Fatalf("no fire before the quiet window elapses; got %v", fired)
	}
	if ft == nil || ft.resets != 2 {
		t.Fatalf("expected 1 create + 2 resets; timer=%v", ft)
	}

	ft.fire() // quiet window elapses

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != "c" {
		t.Fatalf("a burst must collapse into one fire carrying the last payload; got %v", fired)
	}
}

func TestArmIndependentKeys(t *testing.T) {
	timers := map[int]*fakeTimer{}
	var idx int
	keyOf := map[*fakeTimer]int{}
	nt := func(_ time.Duration, f func()) Timer {
		ft := &fakeTimer{f: f}
		timers[idx] = ft
		keyOf[ft] = idx
		return ft
	}
	var mu sync.Mutex
	fired := map[int]string{}
	d := New(nt,
		func(int) time.Duration { return time.Second },
		func(key int, payload string) { mu.Lock(); fired[key] = payload; mu.Unlock() },
	)

	idx = 0
	d.Arm(0, "zero")
	idx = 1
	d.Arm(1, "one")

	timers[0].fire()
	timers[1].fire()

	mu.Lock()
	defer mu.Unlock()
	if fired[0] != "zero" || fired[1] != "one" {
		t.Fatalf("keys must debounce independently; got %v", fired)
	}
}
