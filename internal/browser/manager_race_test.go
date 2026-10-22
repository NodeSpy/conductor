package browser

import (
	"sync"
	"testing"
)

// running() reads every session's anon/login pointers under m.mu to enforce
// MaxBrowsers, while a session starts or stops its own browser under its
// s.mu. Those writes go through setSlot (m.mu) so the two can't race. Run
// under -race: a bare pointer write would trip the detector here.
func TestRunningNoRaceOnSessionSlots(t *testing.T) {
	m := &Manager{sessions: map[string]*session{}}
	sA := &session{id: "bA"}
	sB := &session{id: "bB"}
	m.sessions["A"] = sA
	m.sessions["B"] = sB
	fake := &Instance{proc: &Process{done: make(chan struct{})}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			_ = m.running()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			// Session B starts then stops its anonymous browser, the way
			// instance()/closeSession mutate the slot — under s.mu, via setSlot.
			sB.mu.Lock()
			m.setSlot(&sB.anon, fake)
			m.setSlot(&sB.anon, nil)
			sB.mu.Unlock()
		}
	}()
	wg.Wait()
}
