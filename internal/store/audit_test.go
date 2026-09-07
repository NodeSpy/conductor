package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditRotation(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")
	s, err := Open(Options{
		StatePath: filepath.Join(dir, "s.json"), AuditPath: auditPath,
		TTL: time.Hour, MaxPRs: 100, AuditMaxSize: 200, // tiny so it rotates fast
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 50; i++ {
		s.Audit(map[string]any{"event": "dispatch", "n": i, "pad": "xxxxxxxxxxxxxxxxxxxx"})
	}

	if _, err := os.Stat(auditPath); err != nil {
		t.Fatalf("live audit log missing: %v", err)
	}
	if _, err := os.Stat(auditPath + ".1"); err != nil {
		t.Fatalf("rotated audit log (.1) missing — rotation did not trigger: %v", err)
	}
	// The live log stays under the cap (plus one final line).
	fi, _ := os.Stat(auditPath)
	if fi.Size() > 400 {
		t.Fatalf("live audit log too large after rotation: %d bytes", fi.Size())
	}
}

// REGRESSION (backstop): callers redact the audit fields they know about;
// the writer scrubs every string VALUE at write time so a forgotten field
// (an err.Error() embedding a URL-borne secret) can't reach disk cleartext.
// Values are redacted pre-marshal, so secrets containing quotes/backslashes
// (escaped by the JSON encoder) still match.
func TestAuditWriterRedactsValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	a, err := openAudit(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	secret := `quo"ted\secret-XYZZY`
	a.redact = func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") }
	a.write(map[string]any{
		"event": "step_error",
		"error": `Get "https://api.example/?key=` + secret + `": tls fail`,
		"nested": map[string]any{
			"list": []any{"ok", "carries " + secret},
		},
	})
	_ = a.close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The raw secret has JSON-escaped bytes; check both the raw form and the
	// marshaled representation are absent.
	if strings.Contains(string(raw), "XYZZY") {
		t.Fatalf("secret reached the audit file: %s", raw)
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("redaction placeholder missing: %s", raw)
	}
}
