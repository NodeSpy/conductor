package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHoldSet(t *testing.T) {
	var h *HoldSet
	// nil receiver is safe.
	h.Add("x")
	if h.Has("x") {
		t.Fatal("nil HoldSet should never hold")
	}

	h = NewHoldSet("")
	h.Add("")     // empty id ignored
	h.Add("a123") // real id
	if !h.Has("a123") {
		t.Fatal("added id should be held")
	}
	if h.Has("") || h.Has("nope") {
		t.Fatal("unheld ids should not report held")
	}

	// keepOnly(nil) must NOT prune (a transient list error).
	h.keepOnly(nil)
	if !h.Has("a123") {
		t.Fatal("keepOnly(nil) must not forget holds")
	}
	// keepOnly with a real set prunes absent ids, keeps present ones.
	h.Add("b456")
	h.keepOnly(map[string]bool{"a123": true})
	if !h.Has("a123") || h.Has("b456") {
		t.Fatal("keepOnly should keep present and drop absent")
	}
}

// fakePaseo writes a stub `paseo` executable that serves canned JSON for the
// reaper's calls and records `archive <id>` invocations to a log file.
func fakePaseo(t *testing.T, agentsJSON string) (bin, archiveLog string) {
	t.Helper()
	dir := t.TempDir()
	archiveLog = filepath.Join(dir, "archived.log")
	bin = filepath.Join(dir, "paseo")
	script := `#!/usr/bin/env bash
case "$1 $2" in
  "ls --json")      echo '` + agentsJSON + `' ;;
  "inspect "*)      echo '{"PendingPermissions":[],"CreatedAt":"2020-01-01T00:00:00Z"}' ;;
  "workspace ls")   echo '[]' ;;
  "archive "*)      echo "$2" >> "` + archiveLog + `" ;;
  "workspace archive") echo "ws:$3" >> "` + archiveLog + `" ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, archiveLog
}

func TestHoldSetPersists(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "holds.json")

	h1 := NewHoldSet(p)
	h1.Add("agent-1")
	h1.Add("agent-2")

	// A fresh set from the same path reloads the held ids (survives a restart).
	h2 := NewHoldSet(p)
	if !h2.Has("agent-1") || !h2.Has("agent-2") {
		t.Fatal("held ids should persist across a new HoldSet from the same path")
	}
	// Pruning (an agent you archived) is persisted too.
	h2.keepOnly(map[string]bool{"agent-1": true})
	h3 := NewHoldSet(p)
	if !h3.Has("agent-1") || h3.Has("agent-2") {
		t.Fatalf("prune should persist: agent-1 kept, agent-2 dropped")
	}
}

// TestHoldSetRemovePersists: Remove (session-affinity eviction) drops the id
// and the change survives a reload.
func TestHoldSetRemovePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "holds.json")
	h := NewHoldSet(path)
	h.Add("a")
	h.Add("b")
	h.Remove("a")
	h.Remove("missing") // no-op
	if h.Has("a") || !h.Has("b") {
		t.Fatalf("remove: a=%v b=%v", h.Has("a"), h.Has("b"))
	}
	h2 := NewHoldSet(path)
	if h2.Has("a") || !h2.Has("b") {
		t.Fatal("removal must persist across a reload")
	}
	var nilSet *HoldSet
	nilSet.Remove("x") // nil-safe
}
