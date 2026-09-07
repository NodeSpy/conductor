package github

import "testing"

func TestRevertRefs(t *testing.T) {
	pr := func(merged bool, title, body string) *prPayload {
		p := &prPayload{Number: 99, Merged: merged, Title: title}
		p.Body = body
		return p
	}
	cases := []struct {
		name string
		pr   *prPayload
		want []int
	}{
		{"github revert flow", pr(true, `Revert "fix the parser"`, "Reverts acme/widget#12"), []int{12}},
		{"multiple", pr(true, "Revert stack", "Reverts acme/widget#12\nReverts acme/widget#13"), []int{12, 13}},
		{"case-insensitive repo", pr(true, "Revert x", "Reverts ACME/Widget#7"), []int{7}},
		{"unmerged revert PR", pr(false, `Revert "x"`, "Reverts acme/widget#12"), nil},
		{"not revert-titled", pr(true, "fix things", "Reverts acme/widget#12"), nil},
		{"cross-repo revert", pr(true, "Revert y", "Reverts other/repo#12"), nil},
		{"bare number mention", pr(true, "Revert z", "this relates to #12"), nil},
		{"self reference", pr(true, "Revert w", "Reverts acme/widget#99"), nil},
		{"nil", nil, nil},
	}
	for _, tc := range cases {
		got := revertRefs("acme/widget", tc.pr)
		if len(got) != len(tc.want) {
			t.Errorf("%s: %v want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: %v want %v", tc.name, got, tc.want)
			}
		}
	}
}
