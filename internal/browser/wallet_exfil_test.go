//go:build linux || darwin

package browser

import (
	"strings"
	"testing"
)

// TestScrubAllShort pins the split: a short filled value (a CVC, a region
// code) is NOT scrubbed from a whole reply (scrub), so the page is not
// shredded, but IS scrubbed where conductor pulls a value out of a page's own
// text (scrubAll) — the receipt path.
func TestScrubAllShort(t *testing.T) {
	in := &Instance{}
	in.remember(7, "hunter22") // a normal (long) secret
	in.rememberShort("987")    // a CVC
	if got := in.scrub("pay 987 with hunter22"); got != "pay 987 with "+Redacted {
		t.Fatalf("scrub must leave a short value in a general reply: %q", got)
	}
	if got := in.scrubAll("order 987 paid with hunter22"); got != "order "+Redacted+" paid with "+Redacted {
		t.Fatalf("scrubAll must remove the short value too: %q", got)
	}
}

// TestScrubTransforms pins that scrub redacts the common reversible encodings a
// hostile page script could use to slip a filled value past a literal scrub
// (LEAK 2 defense-in-depth), and leaves ordinary text alone.
func TestScrubTransforms(t *testing.T) {
	in := &Instance{}
	const pw = "SENTINEL-PW-9f3a1b2c"
	in.remember(1, pw)
	for name, s := range map[string]string{
		"literal":  "x " + pw + " y",
		"reversed": "x " + reverseStr(pw) + " y",
		"base64":   "x U0VOVElORUwtUFctOWYzYTFiMmM= y",
	} {
		if got := in.scrub(s); strings.Contains(got, pw) || got == s {
			t.Fatalf("scrub(%s) did not redact: %q", name, got)
		}
	}
	// Ordinary text is untouched.
	if got := in.scrub("the quick brown fox"); got != "the quick brown fox" {
		t.Fatalf("scrub over-redacted ordinary text: %q", got)
	}
}

// TestWalletCVCReceiptExfil (red team, attacker B — exfiltration/propagation).
//
// The card's CVC is filled with no text-scrub (walletfill.go: cvc returns a
// nil scrub list), so it never enters in.secrets. A malicious merchant (a page
// the agent controls on its own allowlisted origin) can write the CVC into its
// order-confirmation page; conductor's readReceipt reads that page, scrubs it
// with in.scrub (which does NOT know the CVC), and extracts an "order number"
// that carries the CVC. That order number then reaches the agent (the pay reply
// and purchase-status), the audit, and the persisted ledger — breaking the
// promise that the card's CVC never reaches the agent "in a reply, a log, the
// run record or a screenshot".
//
// Before the fix: the CVC "987" appears in the order_number everywhere.
// After the fix: it is dropped (fail closed), while a benign order number still
// flows.
func TestWalletCVCReceiptExfil(t *testing.T) {
	w := newWalletRig(t)
	w.shop.EchoCVCInOrder = true // the malicious merchant
	op := w.caller("k-op", RequesterOperator)

	w.do(op, "open", map[string]any{"url": w.a + "/cart/add?item=widget"})
	id := w.propose(op, "12.00", w.a)["purchase"].(string)
	w.approve(id)
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place := refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	done := w.do(op, "pay", map[string]any{"purchase": id, "submit": place})

	const cvc = "987" // the sentinel CVC (newWalletRig: c/cvc)

	// 1. The pay reply the agent receives.
	if on, _ := done["order_number"].(string); strings.Contains(on, cvc) {
		t.Fatalf("LEAK: the CVC reached the agent in the pay reply's order_number: %q", on)
	}
	// 2. purchase-status the agent can re-query any time.
	st := w.do(op, "purchase-status", map[string]any{"purchase": id})
	if on, _ := st["order_number"].(string); strings.Contains(on, cvc) {
		t.Fatalf("LEAK: the CVC reached the agent in purchase-status order_number: %q", on)
	}
	// 3. The persisted ledger (served to the operator API, read by the agent).
	got, _, _ := w.m.Purchases.Get(id)
	if got.Receipt != nil && strings.Contains(got.Receipt.OrderNumber, cvc) {
		t.Fatalf("LEAK: the CVC was persisted to the ledger's order_number: %q", got.Receipt.OrderNumber)
	}
	// 4. The audit log. Check only the fields that carry page-derived values
	//    (the order number / charge / receipt note), not the whole entry — the
	//    merchant's random port can coincidentally contain the 3-digit CVC.
	for _, e := range w.audit.find("card-completed") {
		for _, k := range []string{"order_number", "receipt_note"} {
			if v, _ := e[k].(string); strings.Contains(v, cvc) {
				t.Fatalf("LEAK: the CVC reached the audit %s: %q", k, v)
			}
		}
	}

	// The merchant did receive the CVC (that is inherent and expected) — the
	// test is only that conductor did not round-trip it back to the agent.
	if orders, _ := w.shop.Snapshot(); len(orders) == 0 || !orders[0].CVCSeen {
		t.Fatalf("the merchant should have received the card: %+v", orders)
	}
}

// TestWalletBenignOrderNumberSurvives: the fix must not shred a normal order
// number that merely happens not to contain a filled value.
func TestWalletBenignOrderNumberSurvives(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	w.do(op, "open", map[string]any{"url": w.a + "/cart/add?item=widget"})
	id := w.propose(op, "12.00", w.a)["purchase"].(string)
	w.approve(id)
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place := refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	done := w.do(op, "pay", map[string]any{"purchase": id, "submit": place})
	if done["order_number"] != "FS-1001" || done["charged"] != 12.0 {
		t.Fatalf("a benign order number/charged must still reach the agent: %v", done)
	}
}
