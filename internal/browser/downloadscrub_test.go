package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A download that EMBEDS a short filled value (a 3-char CVC among other text)
// must be written with that value redacted — not leaked verbatim, and not the
// whole file refused. This is the same substring redaction scrub() applies on
// every other agent-visible path; the download path was the one place a short
// value either leaked (exact-match guard) or sank a real receipt (substring
// refuse). A long value is still refused outright (a file that is the password
// reflected back), and a file with no filled value is written byte-for-byte.
func TestScreenSecretFileScrubsShortEmbedded(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A 3-char CVC embedded in a receipt among ordinary numbers.
	receipt := "Order 5551 total $48.10 — CVC 481 on file, thank you\n"
	got, err := screenSecretFile(write("receipt.txt", receipt), []string{"481"})
	if err != nil {
		t.Fatalf("a receipt embedding a 3-char CVC must be written, not refused: %v", err)
	}
	if strings.Contains(string(got), "481") {
		t.Fatalf("the 3-char CVC leaked into the written file: %q", got)
	}
	if !strings.Contains(string(got), Redacted) {
		t.Fatalf("the CVC was not redacted: %q", got)
	}
	// The rest of the receipt survives (only the value is cut).
	if !strings.Contains(string(got), "Order 5551 total $48.10") || !strings.Contains(string(got), "thank you") {
		t.Fatalf("redaction removed more than the value: %q", got)
	}

	// A long (>=4) value that merely appears in the file is still refused.
	if _, err := screenSecretFile(write("pw.txt", "here is hunter22 ok"), []string{"hunter22"}); err == nil {
		t.Fatal("a file containing a long filled value must be refused")
	}

	// A file with no filled value is returned unchanged (byte-for-byte).
	clean := "nothing secret here, just 999 and 42\n"
	got, err = screenSecretFile(write("clean.txt", clean), []string{"481", "hunter22"})
	if err != nil {
		t.Fatalf("a clean file must not be refused: %v", err)
	}
	if string(got) != clean {
		t.Fatalf("a clean file was altered: %q != %q", got, clean)
	}

	// No secrets at all: unchanged, no error.
	got, err = screenSecretFile(write("c2.txt", clean), nil)
	if err != nil || string(got) != clean {
		t.Fatalf("no-secrets path altered or refused the file: %q err=%v", got, err)
	}
}
