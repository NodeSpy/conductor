package config

import (
	"strings"
	"testing"
)

func TestParseUseBuiltin(t *testing.T) {
	for _, tc := range []struct {
		kind UseKind
		ref  string
	}{
		{UseKindConnector, "github"},
		{UseKindConnector, "slack"},
		{UseKindConnector, "kv"},
		{UseKindRuntime, "paseo"},
		{UseKindRuntime, "acp"},
		{UseKindRuntime, "agent-deck"},
	} {
		u, err := ParseUse(tc.kind, tc.ref)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.kind, tc.ref, err)
		}
		if !u.IsBuiltin() {
			t.Fatalf("%s %s: origin = %s, want builtin", tc.kind, tc.ref, u.Origin)
		}
		if u.Name != tc.ref {
			t.Fatalf("%s %s: name = %q", tc.kind, tc.ref, u.Name)
		}
		if u.IsRemote() {
			t.Fatalf("%s %s: builtin reported remote", tc.kind, tc.ref)
		}
		if u.Source() != "" {
			t.Fatalf("%s %s: builtin has a source %q", tc.kind, tc.ref, u.Source())
		}
	}
}

// A bare name that is not builtin falls through to the OFFICIAL repo, at
// <kind>/<name> — the path the conductor-plugins repo is laid out under.
func TestParseUseOfficialFallthrough(t *testing.T) {
	u, err := ParseUse(UseKindConnector, "sentry")
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginOfficial {
		t.Fatalf("origin = %s", u.Origin)
	}
	if u.Repo != OfficialRepo {
		t.Fatalf("repo = %q", u.Repo)
	}
	if u.Component != "connectors/sentry" {
		t.Fatalf("component = %q", u.Component)
	}
	if u.TagPrefix() != "connectors/sentry/" {
		t.Fatalf("tag prefix = %q", u.TagPrefix())
	}
	if u.Source() != "github.com/NodeSpy/conductor-plugins//connectors/sentry" {
		t.Fatalf("source = %q", u.Source())
	}
	if u.InstallKey() != "connectors/sentry" {
		t.Fatalf("install key = %q", u.InstallKey())
	}

	r, err := ParseUse(UseKindRuntime, "modal")
	if err != nil {
		t.Fatal(err)
	}
	if r.Component != "runtimes/modal" || r.TagPrefix() != "runtimes/modal/" {
		t.Fatalf("runtime component = %q prefix = %q", r.Component, r.TagPrefix())
	}
}

// Builtin beats official: a name that IS builtin never reaches the plugin repo.
func TestParseUseBuiltinBeatsOfficial(t *testing.T) {
	u, err := ParseUse(UseKindConnector, "github")
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginBuiltin {
		t.Fatalf("github resolved to %s, want builtin", u.Origin)
	}
}

func TestParseUseExplicitRepo(t *testing.T) {
	for _, tc := range []struct {
		ref       string
		repo      string
		component string
		name      string
		origin    UseOrigin
		host      string
	}{
		{"acme/conductor-plugins/jira", "acme/conductor-plugins", "jira", "jira", OriginGitHub, "github.com"},
		{"acme/conductor-plugins//jira", "acme/conductor-plugins", "jira", "jira", OriginGitHub, "github.com"},
		{"github.com/acme/conductor-plugins//jira", "acme/conductor-plugins", "jira", "jira", OriginGitHub, "github.com"},
		{"https://github.com/acme/conductor-plugins//jira", "acme/conductor-plugins", "jira", "jira", OriginGitHub, "github.com"},
		{"acme/conductor-jira", "acme/conductor-jira", "", "conductor-jira", OriginGitHub, "github.com"},
		{"acme/plugins/connectors/jira", "acme/plugins", "connectors/jira", "jira", OriginGitHub, "github.com"},
		{"git.corp.example/team/plugins//jira", "team/plugins", "jira", "jira", OriginHost, "git.corp.example"},
	} {
		u, err := ParseUse(UseKindConnector, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if u.Repo != tc.repo || u.Component != tc.component || u.Name != tc.name {
			t.Fatalf("%s: repo=%q component=%q name=%q", tc.ref, u.Repo, u.Component, u.Name)
		}
		if u.Origin != tc.origin || u.Host != tc.host {
			t.Fatalf("%s: origin=%s host=%s", tc.ref, u.Origin, u.Host)
		}
		if !u.IsRemote() {
			t.Fatalf("%s: not reported remote", tc.ref)
		}
	}
}

// "//" and "/" produce an identical parse — the old source: separator still
// reads, it is just no longer required.
func TestParseUseSlashSeparatorEquivalence(t *testing.T) {
	a, err := ParseUse(UseKindConnector, "acme/repo//linear")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseUse(UseKindConnector, "acme/repo/linear")
	if err != nil {
		t.Fatal(err)
	}
	if a.Repo != b.Repo || a.Component != b.Component || a.Name != b.Name || a.Origin != b.Origin {
		t.Fatalf("%+v != %+v", a, b)
	}
}

func TestParseUseLocalPath(t *testing.T) {
	for _, tc := range []struct{ ref, name string }{
		{"./plugins/conductor-jira", "jira"},
		{"../bin/conductor-modal", "modal"},
		{"/opt/conductor/plugins/jira", "jira"},
		{"~/bin/conductor-linear", "linear"},
	} {
		u, err := ParseUse(UseKindConnector, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if u.Origin != OriginLocal {
			t.Fatalf("%s: origin = %s", tc.ref, u.Origin)
		}
		if u.Path != tc.ref {
			t.Fatalf("%s: path = %q", tc.ref, u.Path)
		}
		if u.Name != tc.name {
			t.Fatalf("%s: name = %q, want %q", tc.ref, u.Name, tc.name)
		}
		if u.IsRemote() {
			t.Fatalf("%s: local reported remote", tc.ref)
		}
	}
}

func TestParseUseVersionSuffix(t *testing.T) {
	for _, tc := range []struct {
		ref     string
		version string
		pinned  bool
	}{
		{"sentry@v1.2.3", "v1.2.3", true},
		{"sentry@1.2.3", "1.2.3", true},
		{"sentry@^1.2", "^1.2", false},
		{"sentry@~> 1.2", "~> 1.2", false},
		{"acme/repo/jira@v2.0.0", "v2.0.0", true},
		{"sentry", "", false},
	} {
		u, err := ParseUse(UseKindConnector, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if u.Version != tc.version {
			t.Fatalf("%s: version = %q, want %q", tc.ref, u.Version, tc.version)
		}
		if u.IsPinned() != tc.pinned {
			t.Fatalf("%s: pinned = %v, want %v", tc.ref, u.IsPinned(), tc.pinned)
		}
	}
}

// A builtin has no version to pin, and a local binary is whatever is on disk —
// both refuse an @version rather than silently ignoring it.
func TestParseUseVersionRefused(t *testing.T) {
	if _, err := ParseUse(UseKindConnector, "github@v1.0.0"); err == nil {
		t.Fatal("builtin accepted an @version")
	}
	if _, err := ParseUse(UseKindConnector, "./p/conductor-jira@v1.0.0"); err == nil {
		t.Fatal("local path accepted an @version")
	}
}

// KIND ENFORCEMENT: a builtin runtime named under connectors: (or vice versa)
// is refused with a message that says why, not sent to the plugin repo.
func TestParseUseKindMismatchRefused(t *testing.T) {
	_, err := ParseUse(UseKindConnector, "paseo")
	if err == nil {
		t.Fatal("connectors: use: paseo was accepted")
	}
	if !strings.Contains(err.Error(), "can never be wired as a connector") {
		t.Fatalf("unhelpful error: %v", err)
	}
	if _, err := ParseUse(UseKindRuntime, "github"); err == nil {
		t.Fatal("runtimes: use: github was accepted")
	}
}

func TestParseUseErrors(t *testing.T) {
	for _, ref := range []string{
		"",
		"   ",
		"acme/",
		"git.corp.example/team",
		"bad name",
		"https://acme/repo",
	} {
		if u, err := ParseUse(UseKindConnector, ref); err == nil {
			t.Fatalf("%q parsed to %+v, want error", ref, u)
		}
	}
}

func TestUseKindDir(t *testing.T) {
	if UseKindConnector.Dir() != "connectors" || UseKindRuntime.Dir() != "runtimes" {
		t.Fatal("kind dirs")
	}
}

func TestRegisterBuiltinConnector(t *testing.T) {
	const name = "use-test-fake-type"
	if BuiltinConnector(name) {
		t.Fatal("precondition")
	}
	// Before registration a bare name falls through to the official repo.
	u, err := ParseUse(UseKindConnector, name)
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginOfficial {
		t.Fatalf("origin = %s", u.Origin)
	}
	RegisterBuiltinConnector(name)
	u, err = ParseUse(UseKindConnector, name)
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin != OriginBuiltin {
		t.Fatalf("after register, origin = %s", u.Origin)
	}
	var found bool
	for _, n := range BuiltinNames(UseKindConnector) {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Fatal("BuiltinNames omits a registered type")
	}
}
