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

func TestContainsAndExists(t *testing.T) {
	d := map[string]any{
		"labels": []any{"bug", "p1"},
		"strs":   []string{"x", "y"},
		"title":  "fix the flaky test",
		"n":      int64(3),
		"answer": 42,
		"quoted": "a,b",
	}
	cases := []struct {
		cond string
		want bool
	}{
		{`contains(labels, "bug")`, true},
		{`contains(labels, "nope")`, false},
		{`contains(strs, "y")`, true},
		{`contains(title, "flaky")`, true},
		{`contains(title, quoted)`, false}, // path arg resolving to "a,b"
		{`contains("a,b,c", "b")`, true},   // quoted literal with a comma
		{`contains(n, "3")`, false},        // non-container hay
		{`exists(labels)`, true},
		{`exists(missing.deep)`, false},
		{`!exists(missing)`, true},
		{`n == 3`, true},
		{`answer >= 42`, true},
		{`title == "fix the flaky test"`, true},
	}
	for _, c := range cases {
		got, err := Eval(c.cond, d)
		if err != nil {
			t.Fatalf("Eval(%q): %v", c.cond, err)
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.cond, got, c.want)
		}
	}
	// contains arity error.
	if _, err := Eval(`contains(labels)`, d); err == nil {
		t.Fatal("contains arity must error")
	}
	// empty term error.
	if _, err := Eval(`labels && `, d); err == nil {
		t.Fatal("empty term must error")
	}
}

func TestTruthyAndCoercionShapes(t *testing.T) {
	d := map[string]any{
		"i":   7,
		"i64": int64(8),
		"f":   1.5,
		"s0":  "0",
		"sf":  "false",
		"m":   map[string]any{"k": 1},
		"b":   true,
	}
	for cond, want := range map[string]bool{
		"i": true, "i64": true, "f": true, "b": true, "m": true,
		"s0": false, "sf": false,
		`i > 6`: true, `i64 <= 8`: true, `f == 1.5`: true,
		`s0 == 0`:   true,  // string-number coercion on the left
		`m > 1`:     false, // non-numeric ordering is false
		`b == true`: true,
		`i != 7`:    false,
	} {
		got, err := Eval(cond, d)
		if err != nil {
			t.Fatalf("Eval(%q): %v", cond, err)
		}
		if got != want {
			t.Errorf("Eval(%q) = %v, want %v", cond, got, want)
		}
	}
}
