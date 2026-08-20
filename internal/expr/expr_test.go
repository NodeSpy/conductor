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
