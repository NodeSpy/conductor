package scratch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMkdirTempUsesTheRoot(t *testing.T) {
	r := filepath.Join(t.TempDir(), "state", "tmp")
	SetRoot(r)
	defer SetRoot("")
	d, err := MkdirTemp("x-*")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(d) != r {
		t.Fatalf("%s is not under %s", d, r)
	}
}

func TestSweepRemovesOnlyOldMatches(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	for _, n := range []string{"call-old", "call-new", "other-old"} {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Join(p, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if n != "call-new" {
			os.Chtimes(p, old, old)
		}
	}
	if n := Sweep(dir, "call-", time.Hour); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	for n, want := range map[string]bool{"call-old": false, "call-new": true, "other-old": true} {
		if _, err := os.Stat(filepath.Join(dir, n)); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", n, err == nil, want)
		}
	}
}

func TestSweepUnheldKeepsHeldDirs(t *testing.T) {
	parent := t.TempDir()
	live, err := NewHeld(parent, "tmp-")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Remove()
	dead, err := NewHeld(parent, "tmp-")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dead.Path, "f"), []byte("x"), 0o600)
	dead.lock.Close() // its owner died: the lock is gone, the dir is not
	if n := SweepUnheld(parent, "tmp-", 0); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	if _, err := os.Stat(live.Path); err != nil {
		t.Errorf("a held dir was swept: %v", err)
	}
	if _, err := os.Stat(dead.Path); !os.IsNotExist(err) {
		t.Errorf("an unheld dir survived: %v", err)
	}
	live.Remove()
	if _, err := os.Stat(live.Path); !os.IsNotExist(err) {
		t.Errorf("Remove left the dir: %v", err)
	}
}

func TestCheckSaysWhatToChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := Check(dir); err != nil {
		t.Fatalf("a creatable dir: %v", err)
	}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o600)
	err := Check(filepath.Join(file, "sub"))
	if err == nil || !strings.Contains(err.Error(), "scratch.dir") {
		t.Fatalf("an unusable dir: %v, want an error naming scratch.dir", err)
	}
}
