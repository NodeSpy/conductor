package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// §D: requires.connectors is version-aware, with the bare list as sugar.

func TestConnectorReqsShapes(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		want  ConnectorReqs
		error string
	}{
		{
			name: "list form is sugar for any version",
			yaml: "connectors: [github, sentry]",
			want: ConnectorReqs{"github": AnyVersion, "sentry": AnyVersion},
		},
		{
			name: "map form carries constraints",
			yaml: "connectors: { github: \"*\", jira: \">=2.0\" }",
			want: ConnectorReqs{"github": AnyVersion, "jira": ">=2.0"},
		},
		{
			name: "an empty constraint means any",
			yaml: "connectors: { github: \"\" }",
			want: ConnectorReqs{"github": AnyVersion},
		},
		{
			name: "a bare scalar is one connector",
			yaml: "connectors: github",
			want: ConnectorReqs{"github": AnyVersion},
		},
		{name: "null is empty", yaml: "connectors:", want: nil},
		{name: "empty name in a list", yaml: "connectors: [\"\"]", error: "empty connector name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req PackRequires
			err := strictUnmarshal([]byte(tc.yaml), &req)
			if tc.error != "" {
				if err == nil || !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("want error containing %q, got %v", tc.error, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(tc.want) == 0 {
				if len(req.Connectors) != 0 {
					t.Fatalf("want empty, got %v", req.Connectors)
				}
				return
			}
			if !reflect.DeepEqual(req.Connectors, tc.want) {
				t.Fatalf("got %v, want %v", req.Connectors, tc.want)
			}
		})
	}
}

// The sugar round-trips as sugar: a bare list does not become a map on
// marshal (which the pack lockfile and `pack show` would otherwise churn).
func TestConnectorReqsRoundTrip(t *testing.T) {
	for _, in := range []string{`[github, sentry]`, `{github: "*", jira: ">=2.0"}`} {
		var got ConnectorReqs
		if err := yaml.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		out, err := yaml.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var back ConnectorReqs
		if err := yaml.Unmarshal(out, &back); err != nil {
			t.Fatalf("%s: reparse %q: %v", in, out, err)
		}
		if !reflect.DeepEqual(got, back) {
			t.Fatalf("%s: round trip %v -> %q -> %v", in, got, out, back)
		}
	}
	// An all-any map renders as the list form.
	out, _ := yaml.Marshal(ConnectorReqs{"github": AnyVersion})
	if !strings.Contains(string(out), "- github") {
		t.Fatalf("an all-any set should render as the list sugar, got %q", out)
	}
}

func TestConnectorNames(t *testing.T) {
	r := PackRequires{Connectors: ConnectorReqs{"sentry": AnyVersion, "github": ">=1"}}
	if got := r.ConnectorNames(); !reflect.DeepEqual(got, []string{"github", "sentry"}) {
		t.Fatalf("names = %v", got)
	}
	if got := (PackRequires{}).ConnectorNames(); len(got) != 0 {
		t.Fatalf("no connectors = %v", got)
	}
}

// --- the gate --------------------------------------------------------------

const versionedPack = `
pack:
  name: needs-jira
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    connectors:
      jira: ">=2.0"
`

// A plugin connector below the constraint fails the load, naming both sides.
func TestRequiresConnectorVersionGateRejectsOldPlugin(t *testing.T) {
	SetConnectorVersions(map[string]string{"tickets": "v1.4.0"})
	t.Cleanup(func() { SetConnectorVersions(nil) })

	dir := t.TempDir()
	writePackSource(t, dir, "src/nj", versionedPack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  tickets: { use: acme/plugins/jira }
packs:
  needs-jira:
    source: ./src/nj
    connectors: { jira: tickets }
`))
	if err == nil {
		t.Fatal("a plugin below the constraint must fail the load")
	}
	for _, want := range []string{"needs-jira", "jira", ">=2.0", "tickets", "1.4.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
}

// A plugin at or above the constraint passes.
func TestRequiresConnectorVersionGateAcceptsCurrentPlugin(t *testing.T) {
	SetConnectorVersions(map[string]string{"tickets": "v2.3.1"})
	t.Cleanup(func() { SetConnectorVersions(nil) })

	dir := t.TempDir()
	writePackSource(t, dir, "src/nj", versionedPack)
	if _, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  tickets: { use: acme/plugins/jira }
packs:
  needs-jira:
    source: ./src/nj
    connectors: { jira: tickets }
`)); err != nil {
		t.Fatalf("a satisfying plugin must load: %v", err)
	}
}

// A BUILTIN connector's version is the daemon version — the constraint
// resolves to a scoped daemon-version check.
func TestRequiresConnectorVersionGateUsesDaemonVersionForBuiltins(t *testing.T) {
	prev := runtimeVersion
	t.Cleanup(func() { SetRuntimeVersion(prev); SetConnectorVersions(nil) })
	SetConnectorVersions(nil)

	pack := `
pack:
  name: needs-gh
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    connectors:
      github: ">=9.0"
`
	consumer := `
connectors:
  gh: { use: github, token: x }
packs:
  needs-gh:
    source: ./src/ng
    connectors: { github: gh }
`
	// Daemon below the constraint → refused, naming the daemon version.
	SetRuntimeVersion("v1.2.3")
	dir := t.TempDir()
	writePackSource(t, dir, "src/ng", pack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, consumer))
	if err == nil || !strings.Contains(err.Error(), "1.2.3") {
		t.Fatalf("a builtin should gate on the daemon version, got %v", err)
	}
	// Daemon above it → fine.
	SetRuntimeVersion("v9.1.0")
	dir2 := t.TempDir()
	writePackSource(t, dir2, "src/ng", pack)
	if _, err := resolveAndLoad(t, writeDoc(t, dir2, consumer)); err != nil {
		t.Fatalf("a satisfying daemon must load: %v", err)
	}
}

// The bare-list form gates nothing — it is "any version" by definition.
func TestRequiresConnectorListFormGatesNothing(t *testing.T) {
	SetConnectorVersions(map[string]string{"tickets": "v0.0.1"})
	t.Cleanup(func() { SetConnectorVersions(nil) })

	dir := t.TempDir()
	writePackSource(t, dir, "src/nj", `
pack:
  name: anyver
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    connectors: [jira]
`)
	if _, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  tickets: { use: acme/plugins/jira }
packs:
  anyver:
    source: ./src/nj
    connectors: { jira: tickets }
`)); err != nil {
		t.Fatalf("the list form must not gate: %v", err)
	}
}

// An unknown resolved version does not fail the box — it warns. A dev build
// or an as-yet-uninstalled plugin has nothing to compare against, and
// hard-failing there would crash-loop an auto-updating fleet.
func TestRequiresConnectorUnknownVersionWarnsRatherThanFails(t *testing.T) {
	SetConnectorVersions(nil) // no install state published
	prev := runtimeVersion
	SetRuntimeVersion("dev")
	t.Cleanup(func() { SetRuntimeVersion(prev) })

	dir := t.TempDir()
	writePackSource(t, dir, "src/nj", versionedPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  tickets: { use: acme/plugins/jira }
packs:
  needs-jira:
    source: ./src/nj
    connectors: { jira: tickets }
`))
	if err != nil {
		t.Fatalf("an unknown version must not fail the load: %v", err)
	}
	warns := strings.Join(cfg.PackWarnings(), "\n")
	if !strings.Contains(warns, "not gated") {
		t.Fatalf("the skipped gate should be surfaced: %s", warns)
	}
}
