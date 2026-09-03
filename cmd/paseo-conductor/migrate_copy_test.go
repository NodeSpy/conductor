package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The daemon's live control.sock lives in the state dir; the recursive copy must
// skip it (and any socket/fifo/device), not try to open+copy it — that fails with
// "no such device or address" and previously aborted the whole migration.
func TestCopyTreeSkipsSockets(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(filepath.Join(src, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(src, "control.sock"))
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	defer l.Close()

	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree must skip the socket, not error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "state.json")); err != nil {
		t.Errorf("regular file should have been copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "control.sock")); !os.IsNotExist(err) {
		t.Errorf("socket should have been skipped (absent in dst), got err=%v", err)
	}
}
