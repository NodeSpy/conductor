package config

import "testing"

func TestValidateWatch(t *testing.T) {
	cases := []struct {
		name    string
		w       *WatchSpec
		wantErr bool
	}{
		{"nil is fine", nil, false},
		{
			"bail rule ok",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.merged == true", Uses: "handoff.bail"}}},
			false,
		},
		{
			"missing uses",
			&WatchSpec{On: []WatchRule{{Uses: "handoff.bail"}}},
			true,
		},
		{
			"uses not connector.verb",
			&WatchSpec{Uses: "pr_get", On: []WatchRule{{Uses: "handoff.bail"}}},
			true,
		},
		{
			"no rules",
			&WatchSpec{Uses: "gh.pr_get"},
			true,
		},
		{
			"rule missing uses",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.merged"}}},
			true,
		},
		{
			"rerun_step rule ok",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.rerun_step"}}},
			false,
		},
		{
			"run_workflow rule ok with workflow option",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.run_workflow", Options: map[string]any{"workflow": "review-flow"}}}},
			false,
		},
		{
			"run_workflow without workflow option rejected",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{Uses: "handoff.run_workflow"}}},
			true,
		},
		{
			// Fleet-safety: a pack pinned to the old verb must still validate on
			// a daemon that has moved to rerun_step/run_workflow.
			"deprecated handoff.refresh still accepted",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{If: "pr.head_sha != handoff.pr.head_sha", Uses: "handoff.refresh"}}},
			false,
		},
		{
			"done is not a watch action",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{Uses: "handoff.done"}}},
			true,
		},
		{
			"non-handoff verb rejected",
			&WatchSpec{Uses: "gh.pr_get", On: []WatchRule{{Uses: "gh.pr_close"}}},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWatch("step foo", tc.w)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateWatch err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
