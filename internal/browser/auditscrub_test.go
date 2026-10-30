package browser

import (
	"strings"
	"testing"
)

// TestAuditScrubBeforeWrite (M3): the audit fields that can carry text
// reflected from the page (url, target) or an error string (reason) must be
// scrubbed of filled secret values BEFORE the entry reaches the audit sink.
// The old doWith scrubbed url/target only after m.audit (dead w.r.t. the sink)
// and never scrubbed reason at all.
func TestAuditScrubBeforeWrite(t *testing.T) {
	in := &Instance{}
	in.remember(7, "123")        // a 3-digit CVC
	in.remember(9, "hunter2tok") // a longer secret

	entry := map[string]any{
		"action": "fill",
		"url":    "https://shop.example/pay?cvc=123",
		"target": "field holding hunter2tok",
		"reason": "fill failed near value 123 / hunter2tok",
		"other":  "nothing secret here",
	}
	scrubEntry(in, entry)

	for _, k := range []string{"url", "target", "reason"} {
		v := entry[k].(string)
		if strings.Contains(v, "123") || strings.Contains(v, "hunter2tok") {
			t.Fatalf("M3: a filled value reached the audit entry[%q]: %q", k, v)
		}
		if !strings.Contains(v, Redacted) {
			t.Fatalf("M3: entry[%q] was not redacted: %q", k, v)
		}
	}
	// A nil instance (no browser yet) must be a safe no-op.
	safe := map[string]any{"url": "https://x/123"}
	scrubEntry(nil, safe)
	if safe["url"] != "https://x/123" {
		t.Fatalf("M3: scrubEntry(nil) mutated the entry: %v", safe)
	}
}

// TestAuditSinkReceivesScrubbed (M3) checks the sink end of the path: running
// the exact sequence doWith now runs — scrubEntry(inst, entry) then m.audit —
// the configured Audit callback receives redacted url/target/reason, never the
// raw secret. (The ordering in doWith itself — scrubEntry placed before
// m.audit, not after — is what makes this hold there.)
func TestAuditSinkReceivesScrubbed(t *testing.T) {
	var captured map[string]any
	m := &Manager{Audit: func(e map[string]any) { captured = e }}
	inst := &Instance{}
	inst.remember(3, "987")

	entry := map[string]any{
		"action":  "probe",
		"url":     "https://m.example/r?cvc=987",
		"target":  "cvc 987",
		"outcome": "error",
		"reason":  "declined at value 987",
	}
	scrubEntry(inst, entry)
	m.audit(entry)

	for _, k := range []string{"url", "target", "reason"} {
		v, _ := captured[k].(string)
		if strings.Contains(v, "987") {
			t.Fatalf("M3: the audit sink saw a raw secret in %q: %q", k, v)
		}
	}
}
