package config

import (
	"strings"
	"testing"
)

const autoBindPack = `
pack:
  name: p
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github] }
workflows:
  flow:
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: hi } }]
`

// AUTO-BIND (docs/design/config-surface-refinements.md §3). When the consumer
// has exactly ONE connector of a required type, `connectors: {github: gh}` is
// ceremony carrying no decision — there is nothing else it could mean. Two or
// more and the choice is real, so it stays the operator's.
func TestSoleConnectorOfARequiredTypeIsAutoBound(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", autoBindPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  p: { source: ./src/p }
`))
	if err != nil {
		t.Fatalf("the sole github connector must bind without ceremony: %v", err)
	}
	// The proof is the rewritten reference: the pack's `github.comment` now
	// names the consumer's actual instance.
	step := cfg.Workflows["p/flow"].Steps[0]
	if step.Uses != "gh.comment" {
		t.Fatalf("the pack's verb must be rebound to the sole connector, got %q", step.Uses)
	}
}

// …and the name need not match: matching is by the instance's `use:` TYPE.
func TestAutoBindMatchesByTypeNotName(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", autoBindPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  my-weird-name: { use: github, token: x }
packs:
  p: { source: ./src/p }
`))
	if err != nil {
		t.Fatalf("a differently-NAMED connector of the right type must bind: %v", err)
	}
	if got := cfg.Workflows["p/flow"].Steps[0].Uses; got != "my-weird-name.comment" {
		t.Fatalf("bound by type, got %q", got)
	}
}

// TWO candidates is a real choice, and it stays the operator's. The error
// names them, so the fix is copy-pasteable.
func TestTwoConnectorsOfARequiredTypeStillNeedAnExplicitBinding(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", autoBindPack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  home: { use: github, token: x }
  work: { use: github, token: y }
packs:
  p: { source: ./src/p }
`))
	if err == nil {
		t.Fatal("two connectors of the required type must NOT be guessed between")
	}
	for _, want := range []string{"ambiguous", "home", "work", "connectors: { github:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q so the fix is obvious: %v", want, err)
		}
	}
}

// An EXPLICIT binding always wins — auto-bind only ever fills a gap, so it
// can never redirect a pack the operator already pointed somewhere.
func TestExplicitBindingBeatsAutoBind(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", autoBindPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  home: { use: github, token: x }
  work: { use: github, token: y }
packs:
  p:
    source: ./src/p
    connectors: { github: work }
`))
	if err != nil {
		t.Fatalf("an explicit binding resolves the ambiguity: %v", err)
	}
	if got := cfg.Workflows["p/flow"].Steps[0].Uses; got != "work.comment" {
		t.Fatalf("the explicit binding must win, got %q", got)
	}
}

// Auto-bind is connector PLUMBING, not consent. A pack that can now reach the
// consumer's github connector still fires on no repo until the operator arms
// it and names them — the repo list is the consent, and nothing here supplies
// it.
func TestAutoBindDoesNotArmTriggers(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack:
  name: p
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github] }
triggers:
  - name: watch
    on: github.pull_request
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: hi } }]
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  p: { source: ./src/p }
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var found bool
	for _, tr := range cfg.Triggers {
		if tr.Name != "p/watch" {
			continue
		}
		found = true
		if tr.Enabled == nil || *tr.Enabled {
			t.Error("an auto-bound pack's trigger must still ship DISARMED — " +
				"binding a connector is plumbing, arming it is consent")
		}
		if triggerScopesRepos(&tr) {
			t.Error("auto-bind must not supply a repo list")
		}
	}
	if !found {
		t.Fatal("the pack trigger did not instantiate")
	}
}

// Candidates are counted by resolved TYPE, not by the raw `use:` string.
//
// A version pin is a spelling of the same type, so string comparison saw ONE
// sentry connector where there are two — and silently auto-bound to it instead
// of raising the ambiguity. Conductor would be choosing, on the operator's
// behalf, between two connectors that may hold different credentials and point
// at different projects.
func TestAutoBindCountsCandidatesByTypeNotRawUse(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack:
  name: p
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [sentry] }
workflows:
  flow:
    steps: [{ id: s, type: agent, prompt: p }]
`)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  sentry1: { use: "sentry@^2.0" }
  sentry2: { use: sentry }
packs:
  p: { source: ./src/p }
`))
	if err == nil {
		t.Fatal("a pinned connector is still a connector of that type — two sentry " +
			"connectors must be an ambiguity, not a silent bind to whichever one " +
			"happened to be spelled without a pin")
	}
	for _, want := range []string{"ambiguous", "sentry1", "sentry2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q: %v", want, err)
		}
	}
}

// …and a SINGLE pinned connector still auto-binds: resolving the type must not
// have made the pin invisible in the other direction. (A BUILTIN cannot carry a
// pin at all — `use: github@^1.0` is refused by ParseUse — so this uses a
// plugin type, which is where pins are legal and where the bug lived.)
func TestASolePinnedConnectorStillAutoBinds(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack:
  name: p
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [sentry] }
workflows:
  flow:
    steps: [{ id: s, type: agent, prompt: p }]
`)
	if _, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh:      { use: github, token: x }
  sentry1: { use: "sentry@^2.0" }
packs:
  p: { source: ./src/p }
`)); err != nil {
		t.Fatalf("a sole PINNED connector of the required type must still bind: %v", err)
	}
}

// The types themselves resolve as expected — the unit behind both cases above.
func TestConnectorTypeStripsThePin(t *testing.T) {
	for _, tc := range []struct{ use, want string }{
		{"sentry", "sentry"},
		{"sentry@^2.0", "sentry"},
		{"github", "github"},
		{"github.com/acme/conductor-plugins//sentry", "sentry"},
		{"github.com/acme/conductor-plugins//sentry@v2", "sentry"},
	} {
		if got := connectorType(ConnectorRef{Use: tc.use}); got != tc.want {
			t.Errorf("connectorType(%q) = %q, want %q — a pin or an explicit path is a "+
				"SPELLING of the type, not a different type", tc.use, got, tc.want)
		}
	}
}
