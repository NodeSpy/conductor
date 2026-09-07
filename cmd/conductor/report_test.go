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

func TestTallySpend(t *testing.T) {
	lines := strings.Join([]string{
		`{"ts":"2026-09-05T10:00:00Z","event":"agent_usage","repo":"o/r","kind":"review","workflow":"gh.pr/nightly","tokens":1000,"cost_usd":0.5,"approximate":false}`,
		`{"ts":"2026-09-05T11:00:00Z","event":"agent_usage","repo":"o/r","kind":"fix","tokens":500,"cost_usd":0.25,"approximate":true}`,
		`{"ts":"2026-09-06T09:00:00Z","event":"agent_usage","repo":"o/x","kind":"fix","tokens":200,"cost_usd":0.1}`,
		`{"ts":"2026-09-05T12:00:00Z","event":"budget_shed","scope":"global","reason":"$5 of $5"}`,
		`{"ts":"2020-01-01T00:00:00Z","event":"agent_usage","repo":"old/old","tokens":9999,"cost_usd":99}`, // before cutoff
		`{"event":"dispatch","kind":"review","outcome":"ok"}`,                                              // not a usage row
		`not json`,
	}, "\n")
	cutoff := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s, err := tallySpend(strings.NewReader(lines), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if s.Runs != 3 || s.Tokens != 1700 || s.USD != 0.85 {
		t.Fatalf("totals: %+v", s)
	}
	if s.ApproxRuns != 1 || s.Sheds != 1 {
		t.Fatalf("approx/sheds: %+v", s)
	}
	if c := s.ByRepo["o/r"]; c == nil || c.Tokens != 1500 || c.Runs != 2 {
		t.Fatalf("by repo: %+v", s.ByRepo["o/r"])
	}
	// A workflow scope groups under its name; kind is the fallback.
	if c := s.ByKind["gh.pr/nightly"]; c == nil || c.Tokens != 1000 {
		t.Fatalf("by workflow: %+v", s.ByKind)
	}
	if c := s.ByKind["fix"]; c == nil || c.Runs != 2 {
		t.Fatalf("by kind fallback: %+v", s.ByKind)
	}
	if c := s.ByDay["2026-09-05"]; c == nil || c.Runs != 2 {
		t.Fatalf("by day: %+v", s.ByDay)
	}
}

func TestTallyQuality(t *testing.T) {
	lines := strings.Join([]string{
		`{"ts":"2026-09-05T10:00:00Z","event":"outcome","agent":"fixer","outcome":"merged","cost_usd":1.5}`,
		`{"ts":"2026-09-05T11:00:00Z","event":"outcome","agent":"fixer","outcome":"merged","cost_usd":0.5}`,
		`{"ts":"2026-09-05T12:00:00Z","event":"outcome","agent":"fixer","outcome":"reverted"}`,
		`{"ts":"2026-09-05T13:00:00Z","event":"outcome","agent":"fixer","outcome":"closed"}`,
		`{"ts":"2026-09-05T14:00:00Z","event":"outcome","agent":"reviewer","outcome":"rejected"}`,
		`{"ts":"2026-09-05T15:00:00Z","event":"gate","agent":"fixer","outcome":"pass"}`,
		`{"ts":"2026-09-05T15:01:00Z","event":"gate","agent":"fixer","outcome":"fail"}`,
		`{"ts":"2026-09-05T15:02:00Z","event":"gate","agent":"fixer","outcome":"escalated"}`,
		`{"ts":"2020-01-01T00:00:00Z","event":"outcome","agent":"fixer","outcome":"merged"}`, // pre-cutoff
	}, "\n")
	q, err := tallyQuality(strings.NewReader(lines), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	f := q["fixer"]
	if f.Merged != 2 || f.Reverted != 1 || f.Closed != 1 || f.GatePassed != 1 || f.GateEscalated != 1 {
		t.Fatalf("fixer cell: %+v", f)
	}
	// accept = merged / (merged+closed+rejected) = 2/3; revert = 1/2.
	if r := f.acceptRate(); r < 0.66 || r > 0.67 {
		t.Fatalf("accept rate: %v", r)
	}
	if r := f.revertRate(); r != 0.5 {
		t.Fatalf("revert rate: %v", r)
	}
	// cost per merged change = (1.5+0.5)/2.
	if f.MergedCost/float64(f.Merged) != 1.0 {
		t.Fatalf("cost/merged: %v", f.MergedCost)
	}
	if q["reviewer"].Rejected != 1 {
		t.Fatalf("reviewer cell: %+v", q["reviewer"])
	}
}
