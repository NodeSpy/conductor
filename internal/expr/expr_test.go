package expr

import "testing"

func data() map[string]any {
	return map[string]any{
		"repo": "acme/w",
		"steps": map[string]any{
			"evaluate": map[string]any{
				"outputs": map[string]any{
					"has_context": true,
					"kind":        "question",
					"score":       float64(7),
				},
			},
		},
	}
}

func TestEval(t *testing.T) {
	d := data()
	cases := []struct {
		cond string
		want bool
	}{
		{"", true},
		{"steps.evaluate.outputs.has_context", true},
		{"!steps.evaluate.outputs.has_context", false},
		{"steps.evaluate.outputs.has_context == true", true},
		{"steps.evaluate.outputs.has_context == false", false},
		{"steps.evaluate.outputs.has_context != false", true},
		{`steps.evaluate.outputs.kind == "question"`, true},
		{`steps.evaluate.outputs.kind == "change"`, false},
		{`steps.evaluate.outputs.kind != "change"`, true},
		{"steps.evaluate.outputs.score == 7", true},
		{"steps.evaluate.outputs.score == 8", false},
		{"steps.evaluate.outputs.score > 5", true},
		{"steps.evaluate.outputs.score > 7", false},
		{"steps.evaluate.outputs.score >= 7", true},
		{"steps.evaluate.outputs.score < 10", true},
		{"steps.evaluate.outputs.score < 7", false},
		{"steps.evaluate.outputs.score <= 7", true},
		{"steps.evaluate.outputs.score>=8", false}, // no spaces
		{"steps.evaluate.outputs.kind > 3", false}, // non-numeric value → false
		{"steps.evaluate.outputs.score >= 5 && steps.evaluate.outputs.score <= 9", true},
		{"steps.evaluate.outputs.score > 9 || steps.evaluate.outputs.has_context", true},
		{"steps.missing.outputs.x", false},         // missing path is falsey
		{"steps.missing.outputs.x == true", false}, // missing != true
		{`repo == "acme/w"`, true},
		{`repo == "acme/w" && steps.evaluate.outputs.has_context`, true},
		{`repo == "other" || steps.evaluate.outputs.has_context`, true},
		{`repo == "other" && steps.evaluate.outputs.has_context`, false},
		{`steps.evaluate.outputs.kind == "change" || steps.evaluate.outputs.kind == "question"`, true},
	}
	for _, c := range cases {
		got, err := Eval(c.cond, d)
		if err != nil {
			t.Fatalf("Eval(%q) error: %v", c.cond, err)
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.cond, got, c.want)
		}
	}
}

func TestDefaultAndCoalesce(t *testing.T) {
	d := data()
	d["empty"] = ""
	d["zero"] = float64(0)
	d["off"] = false
	d["sev"] = "high"
	cases := []struct {
		cond string
		want bool
	}{
		// default: fallback only when the path is missing or empty.
		{`default(sev, "low") == "high"`, true},
		{`default(missing, "low") == "low"`, true},
		{`default(empty, "low") == "low"`, true},
		{`default(missing, true)`, true},
		{`default(missing, "") == ""`, true},
		// 0 and false are real values, not empties.
		{`default(zero, 5) == 0`, true},
		{`default(off, true)`, false},
		// paths as fallback; a bare unmatched word is NOT a literal fallback.
		{`default(missing, sev) == "high"`, true},
		{`default(missing, nope) == ""`, true},
		// coalesce: first present, non-empty argument.
		{`coalesce(missing, empty, sev) == "high"`, true},
		{`coalesce(missing, "z") == "z"`, true},
		{`coalesce(missing, empty)`, false},
		// bare truthiness + combinators.
		{`coalesce(missing, sev) && repo == "acme/w"`, true},
		{`!default(missing, "")`, true},
		// numeric ordering with a defaulted left side.
		{`default(missing, 4) > 3`, true},
		{`default(zero, 4) > 3`, false},
	}
	for _, c := range cases {
		got, err := Eval(c.cond, d)
		if err != nil {
			t.Fatalf("Eval(%q) error: %v", c.cond, err)
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.cond, got, c.want)
		}
	}

	// Arity errors surface, not silently false.
	if _, err := Eval(`default(missing)`, d); err == nil {
		t.Error("default() with one argument should error")
	}
	if _, err := Eval(`coalesce()`, d); err == nil {
		t.Error("coalesce() with no arguments should error")
	}
}
