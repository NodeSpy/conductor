package main

import (
	"strings"
	"testing"
	"time"
)

func TestTallyAudit(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-time.Hour).Format(time.RFC3339)
	old := now.Add(-72 * time.Hour).Format(time.RFC3339)
	lines := strings.Join([]string{
		`{"event":"dispatch","kind":"merge_conflict","outcome":"ok","ts":"` + recent + `"}`,
		`{"event":"dispatch","kind":"merge_conflict","outcome":"failed","ts":"` + recent + `"}`,
		`{"event":"dispatch","kind":"new_comment","ts":"` + recent + `"}`,                // no outcome → ok
		`{"event":"dispatch","kind":"merge_conflict","outcome":"ok","ts":"` + old + `"}`, // aged out
		`{"event":"escalate","kind":"merge_conflict","ts":"` + recent + `"}`,
		`{"event":"needs_input","kind":"review_requested","ts":"` + recent + `"}`,
		`garbage-not-json`,
	}, "\n")

	cutoff := now.Add(-24 * time.Hour)
	dispatch, attention, err := tallyAudit(strings.NewReader(lines), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch["merge_conflict"]["ok"] != 1 || dispatch["merge_conflict"]["failed"] != 1 {
		t.Fatalf("merge_conflict tally wrong: %+v", dispatch["merge_conflict"])
	}
	if dispatch["new_comment"]["ok"] != 1 {
		t.Fatalf("outcome-less row should count as ok: %+v", dispatch["new_comment"])
	}
	if attention["escalate"] != 1 || attention["needs_input"] != 1 {
		t.Fatalf("attention tally wrong: %+v", attention)
	}
}
