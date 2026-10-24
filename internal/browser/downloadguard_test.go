package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// The download guard must not refuse a legitimate file on an incidental 2–3
// char substring of a short credential (a CVC, a 2-letter region): those
// collide with almost any real file — a price, a date, an order number — so a
// substring scan would deny downloads for the rest of a session once one short
// field was filled. Only a value actually present is refused: a long value
// anywhere in the file, or a short value the file IS. Regression guard for the
// round-3 short-secret download guard (#4).
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

	// A legitimate receipt that merely contains "CA" (and not the real secret)
	// downloads fine.
	ok := write("receipt.pdf", "Order #CA-4471\nTotal: CA$42.00 shipped to Canada\n")
	if err := refuseSecretFile(ok, in.secretValues()); err != nil {
		t.Fatalf("a legitimate file with an incidental %q substring was refused: %v", "CA", err)
	}

	// A file actually containing the full credential value is refused.
	leak := write("leak.txt", "your password is SuperSecret123, keep it safe\n")
	if err := refuseSecretFile(leak, in.secretValues()); err == nil {
		t.Fatal("a file containing the filled credential value was not refused")
	}

	// A file that IS the short value (the page offered the region back verbatim)
	// is still refused — the short guard is exact-match, not off.
	isVal := write("region.txt", "CA\n")
	if err := refuseSecretFile(isVal, in.secretValues()); err == nil {
		t.Fatal("a file that is exactly the short filled value was not refused")
	}
}
