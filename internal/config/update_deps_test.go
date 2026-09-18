package config

import "testing"

// DepsEnabled follows Auto when unset, and an explicit deps: overrides either way.
func TestUpdateDepsEnabled(t *testing.T) {
	cases := []struct {
		name string
		auto bool
		deps *bool
		want bool
	}{
		{"unset follows auto=true", true, nil, true},
		{"unset follows auto=false", false, nil, false},
		{"explicit false overrides auto=true", true, boolPtr(false), false},
		{"explicit true overrides auto=false", false, boolPtr(true), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Update{Auto: c.auto, Deps: c.deps}).DepsEnabled(); got != c.want {
				t.Fatalf("DepsEnabled = %v, want %v", got, c.want)
			}
		})
	}
}
