package config

import "testing"

func TestValidatePlugins(t *testing.T) {
	const goodSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	cases := []struct {
		name    string
		p       PluginRef
		wantErr string // substring; "" = valid
	}{
		{"connector ok", PluginRef{Source: "./p", Kind: "connector", Sha256: goodSHA}, ""},
		{"runtime ok", PluginRef{Source: "./p", Kind: "runtime", Provides: "myrt", Sha256: goodSHA}, ""},
		{"unverified opt-in", PluginRef{Source: "./p", Kind: "connector", AllowUnverified: true}, ""},
		{"missing kind", PluginRef{Source: "./p", Sha256: goodSHA}, "missing kind"},
		{"bad kind", PluginRef{Source: "./p", Kind: "widget", Sha256: goodSHA}, "unknown kind"},
		{"missing source", PluginRef{Kind: "connector", Sha256: goodSHA}, "missing source"},
		{"missing sha", PluginRef{Source: "./p", Kind: "connector"}, "missing sha256 pin"},
		{"bad sha", PluginRef{Source: "./p", Kind: "connector", Sha256: "abc"}, "64 hex"},
		{"override bundled runtime", PluginRef{Source: "./p", Kind: "runtime", Provides: "paseo", Sha256: goodSHA}, "bundled runtime"},
		{"glob secret", PluginRef{Source: "./p", Kind: "connector", Sha256: goodSHA, AllowSecrets: []string{"jira/*"}}, "no globs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Plugins: map[string]PluginRef{"jira": tc.p}}
			err := c.validatePlugins()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strContains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func strContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
