package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The per-launch jail roots older releases left in os.TempDir() are removed
// when old and empty; a recent one (a jail possibly still running) and a
// non-empty one are left alone.
func TestSweepLegacyJailRoots(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	mk := func(name string, aged, full bool) string {
		p := filepath.Join(dir, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if full {
			os.WriteFile(filepath.Join(p, "f"), nil, 0o600)
		}
		if aged {
			os.Chtimes(p, old, old)
		}
		return p
	}
	gone := mk("conductor-jail-1", true, false)
	recent := mk("conductor-jail-2", false, false)
	full := mk("conductor-jail-3", true, true)
	other := mk("conductor-code-4", true, false)
	if n := sweepLegacyJailRoots(dir, 24*time.Hour); n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("an old empty jail root survived")
	}
	for _, p := range []string{recent, full, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
}
