package store

import (
	"path/filepath"
	"sync"
	"testing"
)

// planStore opens a store in a fresh directory.
func planStore(t *testing.T) (*Store, Options) {
	t.Helper()
	dir := t.TempDir()
	opts := Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.log")}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, opts
}

// §21: GetPlan returned the struct by value but its Outputs map by
// REFERENCE, so a caller mutating its own result rewrote persisted state —
// and the next save wrote the mutation out as if it had been recorded.
func TestGetPlanDoesNotAliasOutputs(t *testing.T) {
	s, _ := planStore(t)
	if err := s.PutPlan(PlanRecord{RunID: "r1", StepID: "s1", Outputs: map[string]map[string]any{
		"a": {"k": "original"},
	}}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetPlan("r1", "s1")
	if !ok {
		t.Fatal("plan should exist")
	}
	got.Outputs["a"]["k"] = "mutated"
	got.Outputs["b"] = map[string]any{"new": 1}

	again, _ := s.GetPlan("r1", "s1")
	if again.Outputs["a"]["k"] != "original" {
		t.Fatalf("a caller's mutation reached the store: %v", again.Outputs)
	}
	if _, added := again.Outputs["b"]; added {
		t.Fatalf("a caller's new key reached the store: %v", again.Outputs)
	}
}

// §16: two concurrent saves of DIFFERENT keys marshalled under the lock but
// wrote outside it, so renames could land in the opposite order and the
// older snapshot could drop the newer one's record.
func TestConcurrentPlanSavesDoNotLoseRecords(t *testing.T) {
	s, opts := planStore(t)
	var wg sync.WaitGroup
	const n = 40
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.PutPlan(PlanRecord{RunID: "r", StepID: string(rune('a'+i%26)) + string(rune('0'+i/26))})
		}(i)
	}
	wg.Wait()

	// Re-open from disk: the in-memory map is not the thing under test.
	reopened, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := len(reopened.plans); got != n {
		t.Fatalf("on-disk plans = %d, want %d — a concurrent save dropped records", got, n)
	}
}
