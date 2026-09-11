package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ROUND-11 #1 (a PRE-EXISTING pack hole, #53/#55 — not from the scope work).
// requires.connectors is a pack's capability manifest, and it was enforced
// for skill.verbs and nothing else. A pack could ship a PLAIN step
//
//	uses: gh.comment
//
// with no requires.connectors at all: rebindVerb rewrites a connector prefix
// only when the name is declared AND bound, so an undeclared one passed
// through untouched and execVerb resolved it against the consumer's real
// connector. The pack needed only to guess a conventional instance name,
// which is what conventional names are.
func TestPackCannotReachAnUndeclaredConnector(t *testing.T) {
	for _, tc := range []struct{ name, manifest, wantIn string }{
		{
			name:   "a plain step's uses:",
			wantIn: `uses: "gh.comment"`,
			manifest: `
workflows:
  flow:
    steps:
      - { id: s, uses: gh.comment, options: { repo: acme/app, body: hi } }
`,
		},
		{
			name:   "a hook's uses:",
			wantIn: "hook uses:",
			manifest: `
workflows:
  flow:
    steps:
      - id: s
        type: agent
        prompt: p
        hooks:
          - { at: done, uses: slack.post, options: { channel: "#x", text: hi } }
`,
		},
		{
			name:   "a nested (compensate) step's uses:",
			wantIn: `uses: "pagerduty.trigger"`,
			manifest: `
workflows:
  flow:
    steps:
      - id: s
        type: agent
        prompt: p
        compensate: { id: undo, uses: pagerduty.trigger, options: { summary: x } }
`,
		},
		{
			name:   "a trigger's source",
			wantIn: `on: "linear.issue"`,
			manifest: `
triggers:
  - name: t
    on: linear.issue
    steps: [{ id: s, type: agent, prompt: p }]
`,
		},
		{
			name:   "a session end_on",
			wantIn: "session end_on:",
			manifest: `
workflows:
  flow:
    steps:
      - id: s
        type: agent
        prompt: p
        session: { key: k, end_on: [gh._closed] }
`,
		},
		{
			name:   "a data built-in is an operator resource too",
			wantIn: `uses: "kv.set"`,
			manifest: `
workflows:
  flow:
    steps:
      - { id: s, uses: kv.set, options: { store: theirs, key: k, value: v } }
`,
		},
		{
			name:   "conductor.* is denied outright",
			wantIn: "daemon control",
			manifest: `
workflows:
  flow:
    steps:
      - { id: s, uses: conductor.restart }
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			man := packWithBody(t, tc.manifest)
			problems := LintPackManifest(man)
			if !containsSubstr(problems, tc.wantIn) {
				t.Fatalf("lint did not refuse the undeclared reference (want %q): %v", tc.wantIn, problems)
			}
		})
	}
}

// …and the same reference DECLARED loads clean, so the boundary is a boundary
// and not a ban.
func TestPackReachesWhatItDeclares(t *testing.T) {
	man := packWithBody(t, `
workflows:
  flow:
    steps:
      - { id: s, uses: gh.comment, options: { repo: acme/app, body: hi } }
`)
	man.Pack.Requires.Connectors = ConnectorReqs{"gh": {Version: AnyVersion, Required: true}}
	if problems := checkPackConnectorRefs(man); len(problems) != 0 {
		t.Fatalf("a declared connector must be reachable: %v", problems)
	}
	// The pack's own orchestration needs no declaration.
	open := packWithBody(t, `
workflows:
  flow:
    steps:
      - { id: s, uses: workflow.run, options: { name: other } }
`)
	if problems := checkPackConnectorRefs(open); len(problems) != 0 {
		t.Fatalf("workflow.* is the pack's own orchestration: %v", problems)
	}
}

// The store selector had the identical shape — rebindStore rewrote a declared
// store and let an undeclared one through to whatever the consumer called by
// that name.
func TestPackCannotTouchAnUndeclaredStore(t *testing.T) {
	man := packWithBody(t, `
workflows:
  flow:
    steps:
      - { id: s, uses: kv.set, options: { store: theirs, key: k, value: v } }
`)
	man.Pack.Requires.Connectors = ConnectorReqs{"kv": {Version: AnyVersion, Required: true}}
	if problems := checkPackStoreRefs(man); !containsSubstr(problems, `store "theirs"`) {
		t.Fatalf("an undeclared store must be refused: %v", problems)
	}
	man.Pack.Requires.Stores = []string{"theirs"}
	if problems := checkPackStoreRefs(man); len(problems) != 0 {
		t.Fatalf("a declared store must be reachable: %v", problems)
	}
}

// The boundary must hold at INSTANTIATE, not only at lint: a hand-authored
// pack that never ran lint is exactly the one that would ship the reach.
func TestPackConnectorBoundaryHoldsAtInstantiate(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/reach", `
pack:
  name: reach
  version: 1.0.0
  requires: { conductor: ">=0.1" }
triggers:
  - name: t
    on: manual
    steps:
      - { id: s, uses: gh.comment, options: { repo: acme/app, body: hi } }
`)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
connectors: { gh: { use: github } }
packs:
  reach: { source: ./src/reach }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAndLoad(t, path)
	if err == nil {
		t.Fatal("a pack reaching an undeclared connector was INSTANTIATED — lint is advisory, " +
			"instantiate is the boundary")
	}
	if !strings.Contains(err.Error(), "requires.connectors") {
		t.Fatalf("the refusal must name the boundary: %v", err)
	}
}

// CLASS-CLOSER. The hole was not "someone forgot gh.comment" — it was that
// ONE reference site (skill.verbs) had the boundary and the others were never
// asked to. This asserts every pack-authored connector-reference site goes
// through the check, by feeding a manifest that names an undeclared connector
// at EACH site and requiring a problem from each. A new site added to Step or
// TriggerSpec without being walked fails here.
func TestEveryPackConnectorRefSiteIsBounded(t *testing.T) {
	sites := map[string]string{
		"step.Uses": `
workflows:
  f: { steps: [ { id: s, uses: undeclared.verb } ] }`,
		"hook.Uses": `
workflows:
  f: { steps: [ { id: s, type: agent, prompt: p, hooks: [ { at: done, uses: undeclared.verb } ] } ] }`,
		"trigger.On": `
triggers:
  - { name: t, on: undeclared.event, steps: [ { id: s, type: agent, prompt: p } ] }`,
		"trigger hook.Uses": `
triggers:
  - name: t
    on: manual
    hooks: [ { at: done, uses: undeclared.verb } ]
    steps: [ { id: s, type: agent, prompt: p } ]`,
		"session.EndOn": `
workflows:
  f: { steps: [ { id: s, type: agent, prompt: p, session: { key: k, end_on: [undeclared.kind] } } ] }`,
		"compensate.Uses": `
workflows:
  f: { steps: [ { id: s, type: agent, prompt: p, compensate: { id: u, uses: undeclared.verb } } ] }`,
		"parallel branch Uses": `
workflows:
  f: { steps: [ { id: s, parallel: [ [ { id: b, uses: undeclared.verb } ] ] } ] }`,
	}
	for site, body := range sites {
		t.Run(site, func(t *testing.T) {
			problems := checkPackConnectorRefs(packWithBody(t, body))
			if !containsSubstr(problems, `"undeclared"`) {
				t.Fatalf("%s is not bounded by requires.connectors — a pack can reach the "+
					"consumer's connectors through it (problems: %v)", site, problems)
			}
		})
	}
}

// packWithBody parses a manifest body with a minimal pack: header.
func packWithBody(t *testing.T, body string) *PackManifest {
	t.Helper()
	var man PackManifest
	src := `
pack:
  name: p
  version: 1.0.0
  requires: { conductor: ">=0.1" }
` + body
	if err := strictUnmarshal([]byte(src), &man); err != nil {
		t.Fatalf("parse manifest: %v\n%s", err, src)
	}
	return &man
}
