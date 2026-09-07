package flow

import (
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// Delivery health (#36 §18): merged-and-not-reverted is the promotion
// signal — a workflow whose changes keep getting reverted rots even when its
// runs "succeed".
func TestSavedWorkflowDeliveryHealth(t *testing.T) {
	st, err := OpenSavedStore(filepath.Join(t.TempDir(), "wf.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save("ship-it", "d", []config.Step{{Uses: "svc.post", Options: map[string]any{"text": "x"}}}, memory.Source{}); err != nil {
		t.Fatal(err)
	}

	// Healthy runs, healthy deliveries.
	for i := 0; i < 3; i++ {
		st.RecordOutcome("ship-it", true)
		st.RecordDelivery("ship-it", true)
	}
	w, _ := st.Get("ship-it")
	if w.Rotting() || w.Deliveries != 3 || w.Reverts != 0 {
		t.Fatalf("healthy: %+v", w)
	}

	// Reverts pile up past half the deliveries → rotting, despite run success.
	st.RecordDelivery("ship-it", false)
	st.RecordDelivery("ship-it", false)
	w, _ = st.Get("ship-it")
	if !w.Rotting() {
		t.Fatalf("reverted-heavy workflow must rot: %+v", w)
	}
	// Unknown names are a no-op.
	st.RecordDelivery("ghost", true)
}
