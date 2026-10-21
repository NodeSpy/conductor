package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShortSecretCoveredEverywhere (H1): a short filled value — a 3-digit CVC,
// a 2-digit expiry part — must not leak to the agent through the general paths
// the old len<4 cutoff excluded: the reply/snapshot/source/evaluate scrub, the
// screenshot rect search (fed by secretValues), and the download guard. The
// exact field is masked by its node id at any length; this is the echo of the
// value *elsewhere* on the page or in a downloaded file.
func TestShortSecretCoveredEverywhere(t *testing.T) {
	in := &Instance{}
	in.remember(7, "123")   // a 3-digit CVC filled into cc-csc
	in.rememberShort("05")  // a 2-digit expiry month (wallet short path)
	in.remember(9, "hunt3") // a normal (>=4) secret still works

	// 1. The general scrub (reply text, snapshot, source, evaluate output).
	if got := in.scrub("CVC 123 exp 05 pw hunt3"); strings.Contains(got, "123") || strings.Contains(got, "05") || strings.Contains(got, "hunt3") {
		t.Fatalf("H1: a short secret leaked through scrub: %q", got)
	}

	// 2. scrubAny over a structured reply (what a snapshot/evaluate reply is).
	out := in.scrubAny(map[string]any{"text": "order shows 123 and 05"}).(map[string]any)
	if s := out["text"].(string); strings.Contains(s, "123") || strings.Contains(s, "05") {
		t.Fatalf("H1: a short secret leaked through scrubAny: %q", s)
	}

	// 3. The screenshot rect search and the download guard both read
	//    secretValues(); it must now carry the short values too.
	vals := strings.Join(in.secretValues(), " ")
	if !strings.Contains(vals, "123") || !strings.Contains(vals, "05") {
		t.Fatalf("H1: secretValues() omits the short values (screenshot/download blind): %v", in.secretValues())
	}

	// 4. The download guard refuses a file echoing the 3-digit CVC.
	f := filepath.Join(t.TempDir(), "receipt.txt")
	if err := os.WriteFile(f, []byte("thanks! cvc=123 on file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refuseSecretFile(f, in.secretValues()); err == nil {
		t.Fatal("H1: a downloaded file containing the 3-digit CVC was not refused")
	}
}
