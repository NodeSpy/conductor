package store

import (
	"os"
	"path/filepath"
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
