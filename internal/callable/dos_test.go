package callable

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestWaitInflightCapDegradesToAsync proves a synchronous ?wait=true past the
// inflight cap does NOT block a server goroutine for the whole wait window — it
// degrades to a 202 async response the caller polls (#36 §13 review, item 5).
// With the single wait slot already held and a long wait_timeout, a normal wait
// would block for wait_timeout; the capped path must return immediately.
func TestWaitInflightCapDegradesToAsync(t *testing.T) {
	cfg := bearerCfg()
	cfg.MaxWaitInflight = 1
	cfg.WaitTimeout = config.Duration(10 * time.Second) // long: a real wait would hang
	h := newHarness(t, cfg, map[string]bool{"triage": true})
	// No completion set — a run that entered the wait path would sit until timeout.

	// Occupy the only wait slot, as a concurrent in-flight wait would.
	h.svc.waitSem <- struct{}{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/invoke/triage?wait=true", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer s3cret")

	done := make(chan struct{})
	start := time.Now()
	go func() { h.svc.handleInvoke(w, r); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("capped wait blocked instead of degrading to async")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("capped wait took %s — it awaited instead of degrading", elapsed)
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("capped wait status = %d, want 202 async; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "accepted" || resp["run_id"] == "" {
		t.Fatalf("capped wait body = %v, want accepted + run_id", resp)
	}
	// The run was still admitted — the cap only changes delivery, not dispatch.
	if len(h.invoked) != 1 {
		t.Fatalf("Invoke ran %d times, want 1 (cap must not drop the run)", len(h.invoked))
	}
}

// TestCallbackInflightCapNotScheduled proves a callback past the inflight cap is
// not scheduled — no unbounded goroutine — and is audited delivered:false with a
// concurrency reason, instead of being silently dropped (#36 §13 review 5).
func TestCallbackInflightCapNotScheduled(t *testing.T) {
	cfg := bearerCfg()
	cfg.MaxCallbackInflight = 1
	h := newHarness(t, cfg, map[string]bool{"triage": true})
	h.completeWith = okRun

	// Occupy the only callback slot.
	h.svc.cbSem <- struct{}{}

	w := httptest.NewRecorder()
	h.svc.handleInvoke(w, invokeReq("triage", "s3cret", `{"callback_url":"https://cb.example/hook"}`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	// No callback should be delivered (the slot is full).
	select {
	case body := <-h.callback:
		t.Fatalf("a callback was delivered past the cap: %s", body)
	case <-time.After(200 * time.Millisecond):
	}

	// And the refusal is audited delivered:false with the concurrency reason.
	var found bool
	h.mu.Lock()
	for _, a := range h.audits {
		if a["event"] == "callable_callback" && a["delivered"] == false && a["reason"] == "callback concurrency limit" {
			found = true
		}
	}
	h.mu.Unlock()
	if !found {
		t.Fatalf("no callable_callback delivered:false audit for the capped callback; audits=%v", h.audits)
	}
}
