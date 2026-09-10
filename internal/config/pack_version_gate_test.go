package config

import (
	"strings"
	"testing"
)

// H2: a monorepo plugin tag keeps its subdir prefix
// ("jira-connector/v1.0.0"). That parsed as unknown-semver, and the gate's
// unparseable branch returned nil SILENTLY — so an incompatible connector
// loaded clean with no warning.
func TestConnectorVersionGateHandlesPrefixedTags(t *testing.T) {
	SetConnectorVersions(map[string]string{"tickets": "jira-connector/v1.0.0"})
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
		t.Fatal("a prefixed tag below the constraint must not load clean")
	}
	for _, want := range []string{">=2.0", "1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
}

// The other half: a version we genuinely cannot parse is now surfaced,
// not swallowed. Only "" and "dev" stay silent.
func TestUnparseableConnectorVersionWarns(t *testing.T) {
	SetConnectorVersions(map[string]string{"tickets": "release-candidate"})
	t.Cleanup(func() { SetConnectorVersions(nil) })

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
		t.Fatalf("an unparseable version must not fail the load: %v", err)
	}
	if w := strings.Join(cfg.PackWarnings(), "\n"); !strings.Contains(w, "release-candidate") {
		t.Fatalf("an ungatable version should be surfaced: %s", w)
	}
}
