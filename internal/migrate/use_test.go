package migrate

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// transformDoc runs the whole migration and decodes the output, so these tests
// assert on the SHAPE a user's file ends up in, not on string matching.
func transformDoc(t *testing.T, in string) (map[string]any, []string) {
	t.Helper()
	res, err := Transform([]byte(in))
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !res.Changed {
		t.Fatalf("expected a change; input:\n%s", in)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(res.Output, &doc); err != nil {
		t.Fatalf("output does not parse: %v\n%s", err, res.Output)
	}
	return doc, res.Summary
}

func entry(t *testing.T, doc map[string]any, block, name string) map[string]any {
	t.Helper()
	b, ok := doc[block].(map[string]any)
	if !ok {
		t.Fatalf("no %s: block in %v", block, doc)
	}
	e, ok := b[name].(map[string]any)
	if !ok {
		t.Fatalf("no %s.%s in %v", block, name, b)
	}
	return e
}

// The headline case: a plugin declaration plus a connector that referenced its
// type collapse into ONE `use:`, and the plugins: block disappears.
func TestMigratePluginAndConnectorCollapse(t *testing.T) {
	doc, summary := transformDoc(t, `
plugins:
  jira:
    source: github.com/acme/conductor-plugins//jira
    kind: connector
    version: "~> 1.2"
    sha256: "aaaa"
    isolation: { mode: namespace }
connectors:
  tickets:
    type: jira
    api_key: xyz
`)
	if _, ok := doc["plugins"]; ok {
		t.Fatalf("plugins: block survived: %v", doc)
	}
	e := entry(t, doc, "connectors", "tickets")
	if e["use"] != "acme/conductor-plugins/jira@~> 1.2" {
		t.Fatalf("use = %v, want the ref with its version folded in", e["use"])
	}
	if _, ok := e["type"]; ok {
		t.Fatalf("type: survived: %v", e)
	}
	if e["api_key"] != "xyz" {
		t.Fatalf("connection config lost: %v", e)
	}
	// The plugin's isolation carries onto the entry that now references it.
	if _, ok := e["isolation"]; !ok {
		t.Fatalf("isolation not carried onto the connector: %v", e)
	}
	joined := strings.Join(summary, "\n")
	if !strings.Contains(joined, "sha256 dropped") {
		t.Fatalf("dropping sha256 was not reported:\n%s", joined)
	}
}

// A bundled type: is simply renamed — no plugin involved.
func TestMigrateBundledTypeBecomesUse(t *testing.T) {
	doc, _ := transformDoc(t, "connectors:\n  gh:\n    type: github\n    app_id: \"1\"\n")
	e := entry(t, doc, "connectors", "gh")
	if e["use"] != "github" || e["app_id"] != "1" {
		t.Fatalf("entry = %v", e)
	}
}

// A runtime-kind plugin nothing referenced still gets a home, so removing
// plugins: loses nothing.
func TestMigrateRuntimePluginMaterializes(t *testing.T) {
	doc, summary := transformDoc(t, `
plugins:
  modal:
    source: github.com/acme/conductor-plugins//modal
    kind: runtime
    provides: modal-gpu
connectors:
  gh: { type: github }
`)
	e := entry(t, doc, "runtimes", "modal-gpu")
	if e["use"] != "acme/conductor-plugins/modal" {
		t.Fatalf("runtime use = %v", e["use"])
	}
	if !strings.Contains(strings.Join(summary, "\n"), "runtimes.modal-gpu") {
		t.Fatalf("materialization not reported: %v", summary)
	}
}

// Runtime shapes: a builtin type: renames, and an ACP `agent:` gains the
// `use: acp` that was previously implicit.
func TestMigrateRuntimeShapes(t *testing.T) {
	doc, _ := transformDoc(t, `
connectors:
  gh: { type: github }
runtimes:
  local:  { type: paseo, bin: paseo, default: true }
  gemini: { agent: gemini }
`)
	local := entry(t, doc, "runtimes", "local")
	if local["use"] != "paseo" || local["bin"] != "paseo" || local["default"] != true {
		t.Fatalf("paseo runtime = %v", local)
	}
	g := entry(t, doc, "runtimes", "gemini")
	if g["use"] != "acp" {
		t.Fatalf("an agent: runtime should become use: acp, got %v", g)
	}
	if g["agent"] != "gemini" {
		t.Fatalf("agent: must be kept alongside use: acp, got %v", g)
	}
}

// A LOCAL plugin source passes straight through as a local `use:` path.
func TestMigrateLocalPluginSource(t *testing.T) {
	doc, _ := transformDoc(t, `
plugins:
  jira:
    source: ./plugins/conductor-jira
    kind: connector
    sha256: "bbbb"
    allow_unsandboxed: true
connectors:
  tickets: { type: jira }
`)
	e := entry(t, doc, "connectors", "tickets")
	if e["use"] != "./plugins/conductor-jira" {
		t.Fatalf("local use = %v", e["use"])
	}
}

// Every retired plugins: field is reported with what replaced it, so the
// operator knows whether they need to act.
func TestMigrateReportsRetiredFields(t *testing.T) {
	_, summary := transformDoc(t, `
plugins:
  jira:
    source: github.com/acme/p//jira
    kind: connector
    sha256: "cccc"
    allow_unverified: true
    allow_unsandboxed: true
    hold: true
    args: ["--x"]
connectors:
  tickets: { type: jira }
`)
	joined := strings.Join(summary, "\n")
	for _, want := range []string{
		"sha256 dropped", "allow_unverified dropped", "allow_unsandboxed dropped",
		"hold dropped", "args dropped",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("summary missing %q:\n%s", want, joined)
		}
	}
	// The hold note must point at the replacement, not just say "gone".
	if !strings.Contains(joined, "@v1.2.3") {
		t.Errorf("the hold note does not name the exact-pin replacement:\n%s", joined)
	}
}

// Migration is IDEMPOTENT: running it again changes nothing. This is what makes
// it safe on every boot.
func TestMigrateUseIsIdempotent(t *testing.T) {
	in := "connectors:\n  gh: { type: github }\nruntimes:\n  r: { type: paseo }\n"
	res, err := Transform([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Transform(res.Output)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatalf("a second migration changed the file again:\n%s", again.Output)
	}
}

// `type:` OUTSIDE connectors:/runtimes: is not this pass's business: stores:,
// vaults:, memory:, step type:, and a connector's nested auth: all keep it.
func TestMigrateLeavesOtherTypesAlone(t *testing.T) {
	doc, _ := transformDoc(t, `
connectors:
  api:
    type: rest
    base_url: https://x.example
    auth: { type: bearer, token: t }
stores:
  cache: { type: boltdb }
vaults:
  house: { type: conductor }
triggers:
  - on: api.tick
    steps: [ { id: s, type: command, command: [x] } ]
`)
	api := entry(t, doc, "connectors", "api")
	if api["use"] != "rest" {
		t.Fatalf("connector type not migrated: %v", api)
	}
	auth, _ := api["auth"].(map[string]any)
	if auth["type"] != "bearer" {
		t.Fatalf("a nested auth type: was rewritten: %v", auth)
	}
	if entry(t, doc, "stores", "cache")["type"] != "boltdb" {
		t.Fatalf("a stores: type: was rewritten: %v", doc["stores"])
	}
	if entry(t, doc, "vaults", "house")["type"] != "conductor" {
		t.Fatalf("a vaults: type: was rewritten: %v", doc["vaults"])
	}
	steps := doc["triggers"].([]any)[0].(map[string]any)["steps"].([]any)
	if steps[0].(map[string]any)["type"] != "command" {
		t.Fatalf("a step type: was rewritten: %v", steps)
	}
}

// A plugin entry with no source: cannot become a reference. It is reported
// rather than silently dropped — and the rest of the file still migrates.
func TestMigrateSourcelessPluginReported(t *testing.T) {
	doc, summary := transformDoc(t, `
plugins:
  broken: { kind: connector }
connectors:
  gh: { type: github }
`)
	if entry(t, doc, "connectors", "gh")["use"] != "github" {
		t.Fatal("the rest of the file did not migrate")
	}
	if !strings.Contains(strings.Join(summary, "\n"), "no source:") {
		t.Fatalf("a sourceless plugin was dropped in silence: %v", summary)
	}
}

func TestSourceToUse(t *testing.T) {
	for _, tc := range []struct{ src, ver, want string }{
		{"github.com/acme/repo//jira", "", "acme/repo/jira"},
		{"github.com/acme/repo//jira", "~> 1.0", "acme/repo/jira@~> 1.0"},
		{"https://github.com/acme/repo//jira", "", "acme/repo/jira"},
		{"acme/repo", "v2.0.0", "acme/repo@v2.0.0"},
		{"./bin/conductor-jira", "1.0", "./bin/conductor-jira"},
		{"", "1.0", ""},
	} {
		if got := sourceToUse(tc.src, tc.ver); got != tc.want {
			t.Errorf("sourceToUse(%q,%q) = %q, want %q", tc.src, tc.ver, got, tc.want)
		}
	}
}
