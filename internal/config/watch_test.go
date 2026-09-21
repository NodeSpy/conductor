package config

import "testing"

func TestValidateWatch(t *testing.T) {
	// A fact step reads the subject; action steps react. Uses the new steps shape.
	fact := Step{ID: "pr", Uses: "gh.pr_get"}
	cases := []struct {
		name    string
		w       *WatchSpec
		wantErr bool
	}{
		{"nil is fine", nil, false},
		{
			"bail action ok",
			&WatchSpec{Steps: []Step{fact, {If: "pr.merged == true", Uses: "handoff.bail"}}},
			false,
		},
		{
			"rerun action ok",
			&WatchSpec{Steps: []Step{fact, {If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.rerun"}}},
			false,
		},
		{
			"workflow action ok",
			&WatchSpec{Steps: []Step{fact, {If: "pr.head_sha != handoff.pr.head_sha", Workflow: "review-flow", With: map[string]any{"pr": "{{.number}}"}}}},
			false,
		},
		{
			"no steps",
			&WatchSpec{},
			true,
		},
		{
			"done is not a watch action",
			&WatchSpec{Steps: []Step{fact, {Uses: "handoff.done"}}},
			true,
		},
		{
			"raw run_workflow verb rejected (use workflow: step)",
			&WatchSpec{Steps: []Step{fact, {Uses: "handoff.run_workflow"}}},
			true,
		},
		// --- old shape normalizes into steps and still validates (fleet-safety) ---
		{
			"deprecated old shape: bail",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.merged == true", Uses: "handoff.bail"}}},
			false,
		},
		{
			"deprecated old shape: run_workflow → workflow step",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.run_workflow", Options: map[string]any{"workflow": "review-flow"}}}},
			false,
		},
		{
			"deprecated old shape: refresh → rerun",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.refresh"}}},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWatch("step foo", tc.w, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateWatch err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestWatchNormalizeOldShape locks the old→new rewrite the deploy-safety relies on.
func TestWatchNormalizeOldShape(t *testing.T) {
	w := &WatchSpec{
		Uses: "gh.pr_get",
		On: []WatchRule{
			{If: "pr.merged", Uses: "handoff.bail", Options: map[string]any{"notify": "x"}},
			{If: "moved", Uses: "handoff.run_workflow", Options: map[string]any{"workflow": "review-flow", "with": map[string]any{"pr": 1}, "notify": "y"}},
			{If: "changed", Uses: "handoff.refresh"},
		},
	}
	w.normalize()
	if w.Uses != "" || w.On != nil {
		t.Fatal("old fields should be cleared after normalize")
	}
	if len(w.Steps) != 4 {
		t.Fatalf("want 4 steps (1 fact + 3 actions), got %d", len(w.Steps))
	}
	if w.Steps[0].ID != "pr" || w.Steps[0].Uses != "gh.pr_get" {
		t.Fatalf("fact step wrong: %+v", w.Steps[0])
	}
	if w.Steps[1].Uses != "handoff.bail" {
		t.Fatalf("bail step wrong: %+v", w.Steps[1])
	}
	if w.Steps[2].Uses != "" || w.Steps[2].Workflow != "review-flow" {
		t.Fatalf("run_workflow should become a workflow: step, got %+v", w.Steps[2])
	}
	if w.Steps[3].Uses != "handoff.rerun" {
		t.Fatalf("refresh should become handoff.rerun, got %+v", w.Steps[3])
	}
	// idempotent
	w.normalize()
	if len(w.Steps) != 4 {
		t.Fatalf("normalize not idempotent: %d", len(w.Steps))
	}
}
