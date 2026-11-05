package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestContinueOnErrorYAML(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    ContinueOnError
		wantErr bool
	}{
		{"true", ContinueOnError{Always: true}, false},
		{"false", ContinueOnError{}, false},
		{`"fix.push.unpushed_commits > 0"`, ContinueOnError{When: "fix.push.unpushed_commits > 0"}, false},
		{`"true"`, ContinueOnError{When: "true"}, false}, // a condition that holds
		{"[x]", ContinueOnError{}, true},
		{"{a: 1}", ContinueOnError{}, true},
	} {
		var s struct {
			C ContinueOnError `yaml:"c,omitempty"`
		}
		err := yaml.Unmarshal([]byte("c: "+tc.in), &s)
		if (err != nil) != tc.wantErr || s.C != tc.want {
			t.Errorf("%s: got %+v, %v", tc.in, s.C, err)
			continue
		}
		if err != nil {
			continue
		}
		// Written back as authored; unset is left out.
		out, err := yaml.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var back struct {
			C ContinueOnError `yaml:"c,omitempty"`
		}
		if err := yaml.Unmarshal(out, &back); err != nil || back.C != tc.want {
			t.Errorf("%s: round-trip %q → %+v, %v", tc.in, out, back.C, err)
		}
		if !tc.want.Set() && strings.Contains(string(out), "c:") {
			t.Errorf("%s: an unset continue_on_error was written: %q", tc.in, out)
		}
	}
}
