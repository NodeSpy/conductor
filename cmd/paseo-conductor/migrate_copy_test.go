package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
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

// A migrated config's legacy paseo-conductor paths (App key, state, audit log) must
// be repointed at the conductor dirs so the old dirs become removable.
func TestRewriteConfigPaths(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	in := "app:\n  private_key_path: ~/.config/paseo-conductor/app.pem\n" +
		"store:\n  state_file: ~/.local/state/paseo-conductor/state.json\n" +
		"  audit_log: /home/x/.local/state/paseo-conductor/audit.jsonl\n"
	if err := os.WriteFile(p, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := rewriteConfigPaths(p)
	if err != nil || !changed {
		t.Fatalf("expected a rewrite, changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), "paseo-conductor") {
		t.Errorf("legacy paths remain: %s", got)
	}
	for _, want := range []string{"~/.config/conductor/app.pem", "~/.local/state/conductor/state.json", "/home/x/.local/state/conductor/audit.jsonl"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing repointed path %q in %s", want, got)
		}
	}
	// Idempotent + missing-file no-op.
	if changed, _ := rewriteConfigPaths(p); changed {
		t.Error("second run should be a no-op")
	}
	if changed, err := rewriteConfigPaths(filepath.Join(dir, "nope.yaml")); changed || err != nil {
		t.Errorf("missing config should be a no-op, got changed=%v err=%v", changed, err)
	}
}
