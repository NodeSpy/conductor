package config

import "testing"

// TestApplyTriggerArmOptions: an arm's options deep-merge onto the shipped
// trigger's options (consumer wins per key), so per-instance knobs like
// ignore_checks compose with the pack's defaults (e.g. flaky_rerun).
func TestApplyTriggerArmOptions(t *testing.T) {
	tr := &TriggerSpec{
		Options: map[string]any{
			"flaky_rerun": map[string]any{"enabled": true, "max": 1},
		},
	}
	enabled := true
	arm := TriggerArm{
		Enabled: &enabled,
		Repos:   []string{"o/r"},
		Options: map[string]any{"ignore_checks": []any{"title-validation"}},
	}
	if err := applyTriggerArm(tr, arm); err != nil {
		t.Fatalf("applyTriggerArm: %v", err)
	}
	if tr.Options["flaky_rerun"] == nil {
		t.Fatal("pack's shipped option flaky_rerun was lost")
	}
	ic, ok := tr.Options["ignore_checks"].([]any)
	if !ok || len(ic) != 1 || ic[0] != "title-validation" {
		t.Fatalf("arm's ignore_checks not merged in: %#v", tr.Options["ignore_checks"])
	}
}

// TestMergeArmOptions: a named arm's options deep-merge onto the "*" arm's.
func TestMergeArmOptions(t *testing.T) {
	star := TriggerArm{Options: map[string]any{"a": 1, "b": 2}}
	named := TriggerArm{Options: map[string]any{"b": 3, "c": 4}}
	out := mergeArm(star, named)
	if out.Options["a"] != 1 || out.Options["b"] != 3 || out.Options["c"] != 4 {
		t.Fatalf("merged options wrong: %#v", out.Options)
	}
}
