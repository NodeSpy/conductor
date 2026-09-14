package inbound

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

// A synthetic Target's Number feeds core.Trigger.Key(), which keys dedup,
// session reuse, and outcome records. For a webhook/RSS/Slack/plugin source
// the hash input comes from the PAYLOAD, so the sender chooses it.
//
// At 31 bits a targeted collision was ~2^31 trials — seconds of laptop CPU —
// and a collision means landing on another event's key: suppressing it as a
// duplicate, or joining its session. This asserts the width that makes that
// infeasible, by finding the width empirically rather than trusting a
// constant: a hash that quietly narrows again fails here.
func TestSyntheticNumberIsWideEnoughToResistCollision(t *testing.T) {
	// Feed enough distinct payloads to light up every bit a wide hash uses.
	var seen int
	for i := 0; i < 20000; i++ {
		seen |= SyntheticTarget("webhook:s", fmt.Sprintf("attacker-payload-%d", i)).Number
	}
	bits := 0
	for v := seen; v > 0; v >>= 1 {
		bits++
	}
	// 31 bits was the bug. Demand real headroom over it.
	if bits <= 40 {
		t.Fatalf("synthetic Number spans only ~%d bits — a targeted collision is ~2^%d "+
			"trials, which a webhook sender can simply compute. Widen the hash.", bits, bits)
	}
	// …and stay inside the range JSON round-trips exactly (see numID).
	if bits > 53 {
		t.Fatalf("synthetic Number spans ~%d bits, past float64's exact integer range — "+
			"it is written into map[string]any audit entries and will round", bits)
	}
	if strconv.IntSize < 64 {
		t.Fatalf("int is %d bits on this platform, so the widened hash truncates back "+
			"to a brute-forceable width", strconv.IntSize)
	}
}

// Two distinct payloads must not collide their Key/dedup. The interesting
// part is not that a handful of strings differ — it is that NO pair among
// many does, at a width where the birthday bound makes an accidental
// collision vanishingly unlikely.
func TestDistinctPayloadsDoNotCollideTheirKey(t *testing.T) {
	keyOf := func(dedup string) string {
		return core.Trigger{
			Source: "webhook", Instance: "s",
			// Untrusted, so Key() takes the source-namespaced branch — the
			// one a payload-derived target actually uses.
			Target: SyntheticTarget("webhook:s", dedup),
		}.Key()
	}
	seen := map[string]string{}
	for i := 0; i < 50000; i++ {
		payload := fmt.Sprintf(`{"id":%d,"action":"opened"}`, i)
		k := keyOf(payload)
		if prev, dup := seen[k]; dup {
			t.Fatalf("two distinct payloads share dedup/session key %q:\n  %s\n  %s\n"+
				"a sender who can collide a key can suppress another event as a "+
				"duplicate, or join its session", k, prev, payload)
		}
		seen[k] = payload
	}
	// The same payload is still STABLE — dedup depends on it.
	if a, b := keyOf("same"), keyOf("same"); a != b {
		t.Fatalf("the same payload must key identically: %q vs %q", a, b)
	}
}

// The Number survives a JSON round-trip through map[string]any exactly, which
// is how it reaches the audit log and every report that reads one back.
func TestSyntheticNumberSurvivesJSONRoundTrip(t *testing.T) {
	for i := 0; i < 5000; i++ {
		n := SyntheticTarget("rss:feed", fmt.Sprintf("item-%d", i)).Number
		b, err := json.Marshal(map[string]any{"number": n})
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]any
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		f, ok := back["number"].(float64)
		if !ok {
			t.Fatalf("number decoded as %T", back["number"])
		}
		if int(f) != n {
			t.Fatalf("number %d round-tripped to %d — it exceeds float64's exact "+
				"integer range (2^53) and audit readers will see a different event",
				n, int(f))
		}
		if f > math.MaxInt64 {
			t.Fatalf("number %v out of range", f)
		}
	}
}
