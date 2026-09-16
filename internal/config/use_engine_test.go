package config

import (
	"strings"
	"testing"
)

// An engine reference resolves EXACTLY like a connector reference: builtin
// first, then the official repo's engines/<name>, then an explicit repo.
func TestParseUseEngineResolutionTable(t *testing.T) {
	for _, tc := range []struct {
		ref       string
		origin    UseOrigin
		name      string
		repo      string
		component string
		source    string
	}{
		{ref: "cli", origin: OriginBuiltin, name: "cli"},
		{ref: "js", origin: OriginBuiltin, name: "js"},
		{ref: "go-embed", origin: OriginBuiltin, name: "go-embed"},
		{ref: "risor", origin: OriginBuiltin, name: "risor"},
		{ref: "lua", origin: OriginBuiltin, name: "lua"},
		{
			ref: "foo", origin: OriginOfficial, name: "foo",
			repo: OfficialRepo, component: "engines/foo",
			source: "github.com/" + OfficialRepo + "//engines/foo",
		},
		{
			ref: "acme/wasm", origin: OriginGitHub, name: "wasm",
			repo: "acme/wasm", source: "github.com/acme/wasm",
		},
		{
			ref: "acme/plugins/wasm", origin: OriginGitHub, name: "wasm",
			repo: "acme/plugins", component: "wasm",
			source: "github.com/acme/plugins//wasm",
		},
		{
			ref: "git.corp.example/team/engines//wasm", origin: OriginHost,
			name: "wasm", repo: "team/engines", component: "wasm",
			source: "git.corp.example/team/engines//wasm",
		},
		{ref: "./bin/conductor-wasm", origin: OriginLocal, name: "wasm"},
	} {
		u, err := ParseUse(UseKindEngine, tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if u.Origin != tc.origin {
			t.Errorf("%s: origin = %s, want %s", tc.ref, u.Origin, tc.origin)
		}
		if u.Name != tc.name {
			t.Errorf("%s: name = %q, want %q", tc.ref, u.Name, tc.name)
		}
		if u.Repo != tc.repo {
			t.Errorf("%s: repo = %q, want %q", tc.ref, u.Repo, tc.repo)
		}
		if u.Component != tc.component {
			t.Errorf("%s: component = %q, want %q", tc.ref, u.Component, tc.component)
		}
		if u.Source() != tc.source {
			t.Errorf("%s: source = %q, want %q", tc.ref, u.Source(), tc.source)
		}
	}
}

// The kind's directory is what the official repo lays engines out under and
// what its release tags are prefixed with.
func TestEngineKindDirAndInstallKey(t *testing.T) {
	if got := UseKindEngine.Dir(); got != "engines" {
		t.Fatalf("Dir() = %q, want engines", got)
	}
	u, err := ParseUse(UseKindEngine, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if got := u.InstallKey(); got != "engines/foo" {
		t.Fatalf("InstallKey() = %q, want engines/foo", got)
	}
	if got := u.TagPrefix(); got != "engines/foo/" {
		t.Fatalf("TagPrefix() = %q, want engines/foo/", got)
	}
}

func TestBuiltinEngineRegistry(t *testing.T) {
	for _, n := range []string{"cli", "js", "go-embed", "risor", "lua"} {
		if !BuiltinEngine(n) {
			t.Errorf("%s is not registered as a builtin engine", n)
		}
	}
	if BuiltinEngine("wasm") {
		t.Error("wasm must not be a builtin engine")
	}
	got := strings.Join(BuiltinNames(UseKindEngine), ",")
	if got != "cli,go-embed,js,lua,risor" {
		t.Fatalf("BuiltinNames(engine) = %q", got)
	}
	RegisterBuiltinEngine("zz-test-engine")
	if !BuiltinEngine("zz-test-engine") {
		t.Fatal("RegisterBuiltinEngine did not take")
	}
	u, err := ParseUse(UseKindEngine, "zz-test-engine")
	if err != nil || !u.IsBuiltin() {
		t.Fatalf("a registered engine must short-circuit to builtin: %v %+v", err, u)
	}
	builtinMu.Lock()
	delete(builtinEngines, "zz-test-engine")
	builtinMu.Unlock()
}

// Adding the engine kind must not move a connector or runtime reference.
// `cli` is the sharpest case: it is a builtin runtime AND a builtin engine,
// and `command` is a builtin connector that an engine name could be mistaken
// for.
func TestEngineKindLeavesOtherKindsAlone(t *testing.T) {
	for _, tc := range []struct {
		kind   UseKind
		ref    string
		origin UseOrigin
		source string
	}{
		{UseKindConnector, "github", OriginBuiltin, ""},
		{UseKindConnector, "command", OriginBuiltin, ""},
		{UseKindRuntime, "cli", OriginBuiltin, ""},
		{UseKindRuntime, "paseo", OriginBuiltin, ""},
		{UseKindConnector, "sentry", OriginOfficial, "github.com/" + OfficialRepo + "//connectors/sentry"},
		{UseKindRuntime, "modal", OriginOfficial, "github.com/" + OfficialRepo + "//runtimes/modal"},
		{UseKindConnector, "acme/repo/jira", OriginGitHub, "github.com/acme/repo//jira"},
	} {
		u, err := ParseUse(tc.kind, tc.ref)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.kind, tc.ref, err)
		}
		if u.Origin != tc.origin || u.Source() != tc.source {
			t.Errorf("%s %s: origin=%s source=%q, want %s / %q", tc.kind, tc.ref, u.Origin, u.Source(), tc.origin, tc.source)
		}
	}
	// js is an engine, not a connector — under connectors: it is still just
	// a bare non-builtin name heading for the official repo, unchanged.
	u, err := ParseUse(UseKindConnector, "js")
	if err != nil {
		t.Fatal(err)
	}
	if u.Component != "connectors/js" {
		t.Fatalf("connector js: component = %q", u.Component)
	}
}

// A builtin engine ships in the binary, so it has no version to pin.
func TestEngineBuiltinRejectsVersion(t *testing.T) {
	_, err := ParseUse(UseKindEngine, "cli@v1.2.3")
	if err == nil || !strings.Contains(err.Error(), "ships in the binary") {
		t.Fatalf("err = %v", err)
	}
}
