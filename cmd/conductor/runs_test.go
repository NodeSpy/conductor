package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/store"
)

// Regression (#140 Q-list): `conductor runs` is the discovery surface for "was
// my run recorded?". A just-recorded run (the newest) must always appear in the
// default listing, an older run past the default cap must not silently vanish —
// the truncation is announced and `--limit 0` surfaces it.
func TestCmdRunsSurfacesRecordedRuns(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
store:
  state_file: `+filepath.Join(dir, "state.json")+`
connectors:
  timer: { type: cron, schedules: { tick: { every: 1h } } }
  box: { type: command }
triggers:
  - on: timer.tick
    steps: [{ id: t, uses: box.run, options: { command: "true" } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	histDir := filepath.Join(dir, "history")

	// More records than the default cap, oldest → newest.
	base := time.Now().Add(-time.Hour)
	total := defaultRunsLimit + 25
	var newest, oldest string
	for i := 0; i < total; i++ {
		id := "r" + string(rune('a'+i/676)) + string(rune('a'+(i/26)%26)) + string(rune('a'+i%26))
		if i == 0 {
			oldest = id
		}
		newest = id
		if err := store.WriteHistory(histDir, store.RunHistory{ID: id,
			Started: base.Add(time.Duration(i) * time.Second), Status: "ok", Repo: "o/r"}); err != nil {
			t.Fatal(err)
		}
	}

	// Default listing: the newest run is present, the oldest (past the cap) is
	// not, and the elision is reported rather than silent.
	out, err := captureStdout(t, func() error { return cmdRuns([]string{"--config", cfgPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, newest) {
		t.Fatalf("default list must show the newest (just-recorded) run %q:\n%s", newest, out)
	}
	if strings.Contains(out, oldest) {
		t.Fatalf("oldest run %q should be past the default cap", oldest)
	}
	if !strings.Contains(out, "not shown") {
		t.Fatalf("truncation must be announced, not silent:\n%s", out)
	}

	// --limit 0 surfaces everything, including the record the cap hid — the same
	// record `conductor runs <id>` can read.
	out, err = captureStdout(t, func() error { return cmdRuns([]string{"--config", cfgPath, "--limit", "0"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, oldest) || !strings.Contains(out, newest) {
		t.Fatalf("--limit 0 must list every recorded run (oldest %q, newest %q):\n%s", oldest, newest, out)
	}
	if strings.Contains(out, "not shown") {
		t.Fatalf("--limit 0 shows all — nothing to announce:\n%s", out)
	}

	// Detail-by-id finds the hidden record too, proving list omission was only
	// the cap, never a missing record.
	if _, err := store.ReadHistory(histDir, oldest); err != nil {
		t.Fatalf("the capped-out run must still be readable by id: %v", err)
	}
}
