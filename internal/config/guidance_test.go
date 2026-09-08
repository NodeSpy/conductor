package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGuidanceSpecUnmarshal(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		want    GuidanceSpec
		wantErr bool
	}{
		{"scalar", `guidance: "one line"`, GuidanceSpec{Parts: []string{"one line"}}, false},
		{"empty scalar", `guidance: ""`, GuidanceSpec{Parts: []string{""}}, false},
		{"list", "guidance:\n  - a\n  - b", GuidanceSpec{Parts: []string{"a", "b"}}, false},
		{"replace scalar", `guidance: { replace: only }`, GuidanceSpec{Parts: []string{"only"}, Replace: true}, false},
		{"replace list", `guidance: { replace: [x, y] }`, GuidanceSpec{Parts: []string{"x", "y"}, Replace: true}, false},
		{"replace empty (disable)", `guidance: { replace: "" }`, GuidanceSpec{Parts: []string{""}, Replace: true}, false},
		{"bad mapping key", `guidance: { append: x }`, GuidanceSpec{}, true},
		{"bad mapping shape", `guidance: { replace: x, extra: y }`, GuidanceSpec{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc struct {
				Guidance GuidanceSpec `yaml:"guidance"`
			}
			err := yaml.Unmarshal([]byte(tc.yaml), &doc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", doc.Guidance)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(doc.Guidance, tc.want) {
				t.Fatalf("got %+v, want %+v", doc.Guidance, tc.want)
			}
		})
	}
}

func TestMergePolicyGuidanceCascade(t *testing.T) {
	g := func(replace bool, parts ...string) *Policy {
		return &Policy{Guidance: &GuidanceSpec{Parts: parts, Replace: replace}}
	}
	parts := func(p *Policy) []string {
		if p == nil || p.Guidance == nil {
			return nil
		}
		return p.Guidance.Parts
	}

	// Scopes stack broadest-first: global + connector + trigger.
	got := MergePolicy(g(false, "G"), g(false, "C"), g(false, "T"))
	if want := []string{"G", "C", "T"}; !reflect.DeepEqual(parts(&got), want) {
		t.Fatalf("stack: got %v, want %v", parts(&got), want)
	}
	// A { replace } trigger resets the accumulation to its own parts.
	got = MergePolicy(g(false, "G"), g(false, "C"), g(true, "T"))
	if want := []string{"T"}; !reflect.DeepEqual(parts(&got), want) {
		t.Fatalf("trigger replace: got %v, want %v", parts(&got), want)
	}
	// A { replace } connector drops global, then a normal trigger stacks on it.
	got = MergePolicy(g(false, "G"), g(true, "C"), g(false, "T"))
	if want := []string{"C", "T"}; !reflect.DeepEqual(parts(&got), want) {
		t.Fatalf("connector replace: got %v, want %v", parts(&got), want)
	}
	// nil scopes skipped; a single scope passes through.
	got = MergePolicy(nil, g(false, "only"), nil)
	if want := []string{"only"}; !reflect.DeepEqual(parts(&got), want) {
		t.Fatalf("single: got %v, want %v", parts(&got), want)
	}
}

func TestGuidanceSpecPrepend(t *testing.T) {
	child := GuidanceSpec{Parts: []string{"child"}}
	got := child.prepend([]string{"parent1", "parent2"})
	want := GuidanceSpec{Parts: []string{"parent1", "parent2", "child"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prepend: got %+v, want %+v", got, want)
	}
	// prepend preserves the child's Replace flag but still stacks (the caller,
	// resolveExtends, is what actually skips inheritance on Replace).
	r := GuidanceSpec{Parts: []string{"c"}, Replace: true}.prepend([]string{"p"})
	if !r.Replace || !reflect.DeepEqual(r.Parts, []string{"p", "c"}) {
		t.Fatalf("prepend should keep Replace and stack parts, got %+v", r)
	}
}
