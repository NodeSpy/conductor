package store

import (
	"sync"
	"testing"
	"time"
)

// A running flow keeps writing its own record's Outputs after PutRun while
// other runs' saves serialize every stored record; with -race this fails if
// the store holds the caller's map.
func TestPutRunDoesNotKeepTheCallersOutputs(t *testing.T) {
	s := newTestStore(t, time.Hour, 10)
	mine := WorkflowRun{ID: "a", Outputs: map[string]map[string]any{}}
	if err := s.PutRun(mine); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			mine.Outputs["step"] = map[string]any{"i": i}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = s.PutRun(WorkflowRun{ID: "b"})
		}
	}()
	wg.Wait()

	for _, r := range s.PendingRuns() {
		if r.ID == "a" && len(r.Outputs) != 0 {
			t.Fatalf("stored record changed with the caller's map: %v", r.Outputs)
		}
	}
}

// A record PendingRuns returns is the caller's to change.
func TestPendingRunsReturnsCopies(t *testing.T) {
	s := newTestStore(t, time.Hour, 10)
	if err := s.PutRun(WorkflowRun{ID: "a", Outputs: map[string]map[string]any{"x": {"k": 1}}}); err != nil {
		t.Fatal(err)
	}
	got := s.PendingRuns()[0]
	got.Outputs["x"]["k"] = 2
	got.Outputs["y"] = nil
	again := s.PendingRuns()[0]
	if again.Outputs["x"]["k"] != 1 || len(again.Outputs) != 1 {
		t.Fatalf("stored record changed through a returned copy: %v", again.Outputs)
	}
}
