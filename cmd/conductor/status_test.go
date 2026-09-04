package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStuckRecords(t *testing.T) {
	recs := map[string]recordLine{
		"a/w#1": {Attempts: map[string]int{"merge_conflict@h1": 4}, AttemptAt: map[string]time.Time{"merge_conflict@h1": time.Now()}},
		"a/w#2": {Attempts: map[string]int{"review_requested@h2": 1}}, // 1 attempt → not stuck
		"a/w#3": {Attempts: map[string]int{"new_comment@h3": 2}},
	}
	got := stuckRecords(recs)
	if len(got) != 2 {
		t.Fatalf("want 2 stuck (attempts>=2), got %d: %+v", len(got), got)
	}
	// Sorted by attempts desc: #1 (4) before #3 (2).
	if got[0].key != "a/w#1" || got[0].kind != "merge_conflict" || got[0].attempts != 4 {
		t.Fatalf("unexpected top stuck: %+v", got[0])
	}
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	var body string
	for i := 0; i < 5; i++ {
		body += `{"n":` + string(rune('0'+i)) + "}\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Small cap forces the seek+drop-partial-first-line path.
	lines := tailLines(p, 12)
	if len(lines) == 0 {
		t.Fatal("expected some tail lines")
	}
	// The last complete line must be present and whole.
	if lines[len(lines)-1] != `{"n":4}` {
		t.Fatalf("last line wrong: %q", lines[len(lines)-1])
	}
	// Full read returns all 5.
	if all := tailLines(p, 1<<20); len(all) != 5 {
		t.Fatalf("full read want 5 lines, got %d", len(all))
	}
}

func TestAgo(t *testing.T) {
	if ago(time.Time{}) != "—" {
		t.Fatal("zero time should render as —")
	}
	if got := ago(time.Now().Add(-90 * time.Minute)); got != "1h ago" {
		t.Fatalf("90m ago = %q, want 1h ago", got)
	}
}
