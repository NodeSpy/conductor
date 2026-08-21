package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHoldSet(t *testing.T) {
	var h *HoldSet
	// nil receiver is safe.
	h.Add("x")
	if h.Has("x") {
		t.Fatal("nil HoldSet should never hold")
	}

	h = NewHoldSet()
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

func TestReaperSparesHeldAgent(t *testing.T) {
	// Both agents are idle, old (past grace), and (pretend) carry archive=1 so the
	// reaper lists them; one is held, one is not.
	agents := `[{"id":"held","status":"idle","cwd":"/tmp/held"},{"id":"other","status":"idle","cwd":"/tmp/other"}]`
	bin, archiveLog := fakePaseo(t, agents)

	hold := NewHoldSet()
	hold.Add("held")
	r := &Reaper{PaseoBin: bin, Held: hold, Log: func(string, ...any) {}}
	r.reap(context.Background())

	data, _ := os.ReadFile(archiveLog)
	got := string(data)
	if strings.Contains(got, "held") {
		t.Fatalf("held hand-off agent must NOT be archived; archive log: %q", got)
	}
	if !strings.Contains(got, "other") {
		t.Fatalf("non-held archive=1 agent should be archived; archive log: %q", got)
	}
	// The held id survives pruning (it's present in the full list).
	if !hold.Has("held") {
		t.Fatal("held id should still be held after a reap tick")
	}
}
