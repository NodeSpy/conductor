package core

import (
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A declared target scope reads a string fact as is and an integer fact (a
// PR number, an int in-process or a JSON float64 off the wire) in decimal,
// reports the declaration's bound flag, and gives nothing for a target the
// sender chose.
func TestTargetScopeFactReadsNumbersAndBound(t *testing.T) {
	sem := &sdk.EventSemantics{Target: &sdk.TargetSemantics{Scope: []sdk.ScopeFact{
		{Dimension: "repo", Fact: "repo"},
		{Dimension: "number", Fact: "number", Bound: true},
		{Dimension: "branch", Fact: "head_ref", Bound: true},
		{Dimension: "wire", Fact: "wire_number"},
	}}}
	tr := Trigger{TargetTrusted: true, Sem: sem, Target: Target{Repo: "acme/app", Number: 42},
		Context: map[string]any{"head_ref": "fix/42", "wire_number": float64(7)}}
	for _, c := range []struct {
		dim, val string
		bound    bool
	}{{"repo", "acme/app", false}, {"number", "42", true}, {"branch", "fix/42", true}, {"wire", "7", false}, {"none", "", false}} {
		if v, b := tr.TargetScopeFact(c.dim); v != c.val || b != c.bound {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.dim, v, b, c.val, c.bound)
		}
	}
	tr.Context["wire_number"] = 7.5
	if v, _ := tr.TargetScopeFact("wire"); v != "" {
		t.Errorf("a fractional number is no scope value, got %q", v)
	}
	tr.TargetTrusted = false
	if v, _ := tr.TargetScopeFact("number"); v != "" {
		t.Errorf("a sender-chosen target implies no scope, got %q", v)
	}
}
