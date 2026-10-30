package browser

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Pure-Go unit coverage for red-team fixes 2, 3 and 4. Until now these were
// exercised only by env-gated real-Chromium tests (redteam_realbrowser_test.go),
// which skip silently without a browser. The suppression and masking logic is
// separable from a live page, so it is tested here directly.

func axVal(s string) *axValue { return &axValue{Value: json.RawMessage(strconv.Quote(s))} }

// node builds one AX node.
func axn(id, parent, role, name string, backend int64, children ...string) axNode {
	return axNode{NodeID: id, ParentID: parent, Role: axVal(role), Name: axVal(name), BackendID: backend, ChildIDs: children}
}

// TestRenderAXSuppressesFilledFieldSubtree covers red-team fixes 2 and 3: a
// filled field's whole accessibility subtree — the StaticText the browser
// mirrors a typed value into (fix 2) and a <select>'s selected-option label
// (fix 3) — is rendered quiet, so the value appears nowhere in the snapshot.
// The suppression is scoped: a NON-filled field's mirror still renders, proving
// the guard keys on the secret node, not on all inputs.
func TestRenderAXSuppressesFilledFieldSubtree(t *testing.T) {
	const (
		regionSecret = "ZZSECRETREGION"
		optionSecret = "QQSECRETOPTION"
		normalText   = "shown@example.com"
	)
	nodes := []axNode{
		axn("0", "", "RootWebArea", "", 0, "label", "field", "sel", "normfield"),
		axn("label", "0", "StaticText", "VisibleLabel", 1),
		// A filled text field (fix 2): its value is already the mark, and its
		// StaticText mirror child must be suppressed.
		axn("field", "0", "textbox", "State", 100, "mirror"),
		axn("mirror", "field", "StaticText", regionSecret, 101),
		// A filled <select> (fix 3): the selected option label mirror child
		// must be suppressed.
		axn("sel", "0", "combobox", "Country", 200, "opt"),
		axn("opt", "sel", "StaticText", optionSecret, 201),
		// A NOT-filled text field: its mirror still renders.
		axn("normfield", "0", "textbox", "Email", 300, "normmirror"),
		axn("normmirror", "normfield", "StaticText", normalText, 301),
	}
	// snapshot() masks the filled fields' own value to the mark before rendering.
	nodes[2].Value = axVal(FilledMark)
	nodes[4].Value = axVal(FilledMark)

	secret := map[int64]bool{100: true, 200: true}
	out, _ := renderAX(nodes, 0, secret)

	if strings.Contains(out, regionSecret) {
		t.Errorf("LEAK: the filled text field's mirrored value is in the snapshot:\n%s", out)
	}
	if strings.Contains(out, optionSecret) {
		t.Errorf("LEAK: the filled select's option label is in the snapshot:\n%s", out)
	}
	if !strings.Contains(out, normalText) {
		t.Errorf("a NON-filled field's mirror was wrongly suppressed (over-broad):\n%s", out)
	}
	if !strings.Contains(out, "VisibleLabel") {
		t.Errorf("an ordinary label went missing:\n%s", out)
	}
	if !strings.Contains(out, FilledMark) {
		t.Errorf("the filled field is not marked:\n%s", out)
	}
}

// TestScrubWaitBlanksMaskedValues covers red-team fix 4 at the masking function:
// scrubWait removes every filled value and displayed label, so the wait
// comparison can never confirm a secret. maskVals carries both the raw value
// and the label a select displays for it.
func TestScrubWaitBlanksMaskedValues(t *testing.T) {
	in := &Instance{maskVals: []string{"s3cr3t-pw", "Illinois"}}
	got := in.scrubWait("welcome s3cr3t-pw, you chose Illinois")
	if strings.Contains(got, "s3cr3t-pw") || strings.Contains(got, "Illinois") {
		t.Fatalf("scrubWait left a masked value in: %q", got)
	}
	if !strings.Contains(got, Redacted) {
		t.Fatalf("scrubWait did not redact: %q", got)
	}
	// A string with nothing masked is returned untouched.
	if s := in.scrubWait("nothing sensitive here"); s != "nothing sensitive here" {
		t.Fatalf("scrubWait altered a benign string: %q", s)
	}
}

// TestWaitIsNotASecretOracle covers red-team fix 4 end-to-end against the fake
// page: `wait text=<secret>` can never succeed, because the body is scrubbed of
// every masked value BEFORE the comparison — so a hostile step cannot use wait
// as a yes/no oracle for what was filled. A wait for ordinary page text still
// succeeds.
func TestWaitIsNotASecretOracle(t *testing.T) {
	in, f := newFakeInstance(t)
	ctx := context.Background()
	tb, err := in.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The page's innerText (what waitFor reads) contains the filled secret.
	f.mu.Lock()
	f.evalValue = json.RawMessage(`"order confirmed for s3cr3t-pw now"`)
	f.mu.Unlock()
	in.mu.Lock()
	in.maskVals = []string{"s3cr3t-pw"}
	in.mu.Unlock()

	// Waiting on the secret must time out — the oracle is blinded.
	if err := in.waitFor(ctx, tb, "s3cr3t-pw", "", "", 400*time.Millisecond); err == nil {
		t.Fatal("wait on a filled secret must not succeed (it would be an oracle)")
	}
	// Waiting on ordinary page text still works.
	if err := in.waitFor(ctx, tb, "order confirmed", "", "", 2*time.Second); err != nil {
		t.Fatalf("wait on ordinary text should succeed: %v", err)
	}
}
