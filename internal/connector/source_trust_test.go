package connector

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// A plugin connector's event trust follows the official-trust model plugin
// installs already use (config.IsOfficialSource), with the integrity
// precondition that the binary is the one the release published.
func TestSourceTrustedFollowsTheOfficialModel(t *testing.T) {
	yes, no := true, false
	spec := func(use string, local, verified bool) plugin.Spec {
		u, err := config.ParseUse(config.UseKindConnector, use)
		if err != nil {
			t.Fatalf("ParseUse(%q): %v", use, err)
		}
		s := plugin.Spec{Name: u.Name, Use: u, Local: local}
		if !local {
			s.Sha256, s.ReleaseVerified = "abc123", verified
		}
		return s
	}
	cases := []struct {
		name     string
		explicit *bool
		spec     plugin.Spec
		want     bool
	}{
		// The official repo, as a bare name would resolve and as an explicit path.
		{"official, verified, default", nil, spec("NodeSpy/conductor-plugins/connectors/github", false, true), true},
		{"official (host-qualified), verified, default", nil, spec("github.com/NodeSpy/conductor-plugins//connectors/github", false, true), true},
		{"official, verified, opted out", &no, spec("NodeSpy/conductor-plugins/connectors/github", false, true), false},
		// Integrity precondition: an unverified official binary is not trusted.
		{"official, NOT release-verified", nil, spec("NodeSpy/conductor-plugins/connectors/github", false, false), false},
		{"official, no recorded sha", nil, func() plugin.Spec {
			s := spec("NodeSpy/conductor-plugins/connectors/github", false, true)
			s.Sha256 = ""
			return s
		}(), false},
		// Third-party and local.
		{"third-party, default", nil, spec("acme/plugins/github", false, true), false},
		{"third-party, granted", &yes, spec("acme/plugins/github", false, true), true},
		{"local, default", nil, spec("./bin/conductor-github", true, false), false},
		{"local, granted (dev loop)", &yes, spec("./bin/conductor-github", true, false), true},
		// Lookalikes: never official.
		{"lookalike: suffixed repo", nil, spec("NodeSpy/conductor-plugins-evil/connectors/github", false, true), false},
		{"lookalike: prefix repo", nil, spec("NodeSpy/conductor-plugin/connectors/github", false, true), false},
		{"lookalike: other owner", nil, spec("NodeSpy-evil/conductor-plugins/connectors/github", false, true), false},
		{"lookalike: other host", nil, spec("gitlab.com/NodeSpy/conductor-plugins//connectors/github", false, true), false},
		{"lookalike: host prefix", nil, spec("github.com.evil.example/NodeSpy/conductor-plugins//connectors/github", false, true), false},
	}
	for _, c := range cases {
		if got := SourceTrusted(c.explicit, c.spec); got != c.want {
			t.Errorf("%s (source %q): trusted=%v, want %v", c.name, c.spec.Use.Source(), got, c.want)
		}
	}
}
