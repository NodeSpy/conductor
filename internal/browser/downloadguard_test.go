package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The download guard must not refuse a legitimate file on an incidental 2–3
// char substring of a short credential (a CVC, a 2-letter region) — those
// collide with almost any real file — and must not leak the value either. A
// short value is redacted out of the written content (the same substring scrub
// every other agent-visible path uses), never refused and never passed through;
// a long value anywhere refuses the whole file. Regression guard for the
// short-secret download guard (#4).
func TestDownloadGuardShortSecretNoFalsePositive(t *testing.T) {
	in := &Instance{}
	in.rememberShort("CA")           // a 2-letter billing region
	in.remember(9, "SuperSecret123") // a normal (>=4) credential

	write := func(name, body string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A receipt that contains the short value "CA": written (not refused), with
	// every occurrence of the value redacted.
	ok := write("receipt.pdf", "Order #CA-4471\nTotal: CA$42.00\n")
	got, err := screenSecretFile(ok, in.secretValues())
	if err != nil {
		t.Fatalf("a legitimate file carrying a short value was refused: %v", err)
	}
	if strings.Contains(string(got), "CA") {
		t.Fatalf("the short value leaked into the written file: %q", got)
	}

	// A file actually containing the full (long) credential value is refused.
	leak := write("leak.txt", "your password is SuperSecret123, keep it safe\n")
	if _, err := screenSecretFile(leak, in.secretValues()); err == nil {
		t.Fatal("a file containing the filled credential value was not refused")
	}

	// A file that IS the short value (the page offered the region back verbatim)
	// is written with the value redacted — not leaked, not refused.
	isVal := write("region.txt", "CA\n")
	if got, err := screenSecretFile(isVal, in.secretValues()); err != nil || strings.Contains(string(got), "CA") {
		t.Fatalf("a file that is the short value must be written redacted: %q err=%v", got, err)
	}
}
