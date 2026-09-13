package config

import (
	"strings"
	"testing"
)

const instancePack = `
pack:
  name: review
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github] }
checks:
  strict:
    uses: github.comment
    options: { repo: "o/r", number: "1", body: check }
triggers:
  - name: deploy
    on: github.push
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: b } }]
`

func instancesOf(t *testing.T, instance string) []TriggerSpec {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", instancePack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
`+instance))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var out []TriggerSpec
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "review/deploy") {
			out = append(out, tr)
		}
	}
	return out
}

// TRIGGER INSTANCES (docs/design/config-surface-refinements.md §6). The same
// pack trigger, armed more than once with different repos/filters/gates —
// without the pack author shipping two near-identical triggers or the operator
// forking the pack.
func TestArrayTriggerYieldsNDistinctInstances(t *testing.T) {
	got := instancesOf(t, `
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true, repos: [team-b/*], filters: { labels: [urgent] } }
`)
	if len(got) != 2 {
		t.Fatalf("an array of 2 arms must yield 2 instances, got %d", len(got))
	}
	// DISTINCT IDENTITY. Trigger.Key/dedup/session/outcome all key off the
	// trigger name, so two instances sharing one would collide: one's dedup
	// would suppress the other's event, and they would share a session.
	if got[0].Name == got[1].Name {
		t.Fatalf("two instances must not share a name (%q) — dedup, session and "+
			"outcome state all key off it", got[0].Name)
	}
	for i, tr := range got {
		addr, handle := SplitInstanceName(tr.Name)
		if addr != "review/deploy" {
			t.Errorf("instance %d keeps the trigger's address, got %q", i, addr)
		}
		if handle == "" {
			t.Errorf("instance %d carries no instance handle: %q", i, tr.Name)
		}
	}
	// Each instance carries its OWN arming.
	if r := repoList(got[0]); len(r) != 1 || r[0] != "team-a/*" {
		t.Errorf("instance 0 repos: %v", r)
	}
	if r := repoList(got[1]); len(r) != 1 || r[0] != "team-b/*" {
		t.Errorf("instance 1 repos: %v", r)
	}
	if _, ok := got[0].Filters["labels"]; ok {
		t.Error("instance 0 must not see instance 1's filters — a shallow copy " +
			"would have them sharing the map")
	}
}

// The OBJECT form is one instance and keeps the trigger's own name, so nothing
// about an existing config changes.
func TestObjectTriggerIsStillOneUnsuffixedInstance(t *testing.T) {
	got := instancesOf(t, `
    triggers:
      deploy: { enabled: true, repos: [team/app] }
`)
	if len(got) != 1 {
		t.Fatalf("the object form is ONE instance, got %d", len(got))
	}
	if got[0].Name != "review/deploy" {
		t.Fatalf("a single instance keeps the plain trigger name, got %q", got[0].Name)
	}
}

// Per-instance gates: the whole point of arming one trigger twice.
//
// The arm is written by the CONSUMER, so its gate names a check in the
// consumer's namespace — the pack's own `strict` is instantiated as
// `review/strict`, and that is what the operator writes, exactly as they
// address any other pack internal.
func TestInstancesCarryTheirOwnGate(t *testing.T) {
	got := instancesOf(t, `
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*], gate: { run: [review/strict] } }
        - { enabled: true, repos: [team-b/*] }
`)
	if len(got) != 2 {
		t.Fatalf("want 2 instances, got %d", len(got))
	}
	if got[0].Gate == nil || len(got[0].Gate.Run) != 1 || got[0].Gate.Run[0] != "review/strict" {
		t.Errorf("instance 0 must carry its own gate, got %+v", got[0].Gate)
	}
	if got[1].Gate != nil {
		t.Errorf("instance 1 must not inherit instance 0's gate, got %+v", got[1].Gate)
	}
}

// Repo consent is PER INSTANCE: arming three and giving two of them repos must
// not let the third through on the others' consent.
func TestEachInstanceNeedsItsOwnRepoConsent(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", instancePack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true }
`))
	if err == nil || !strings.Contains(err.Error(), "names no repos") {
		t.Fatalf("an armed instance with no repos must be refused on its own account: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "deploy[1]") {
		t.Errorf("the error should name WHICH instance: %v", err)
	}
}

// Instances compose with `"*"`: the wildcard sets defaults for all, and a
// named array refines/adds instances for that one.
func TestInstancesComposeWithArmAll(t *testing.T) {
	got := instancesOf(t, `
    triggers:
      "*": { enabled: true, repos: [org/default] }
      deploy:
        - { repos: [team-a/*] }
        - { filters: { labels: [urgent] } }
`)
	if len(got) != 2 {
		t.Fatalf("want 2 instances, got %d", len(got))
	}
	if r := repoList(got[0]); len(r) != 1 || r[0] != "team-a/*" {
		t.Errorf("instance 0 overrides the wildcard's repos: %v", r)
	}
	// Instance 1 named no repos, so it inherits the wildcard's consent.
	if r := repoList(got[1]); len(r) != 1 || r[0] != "org/default" {
		t.Errorf("instance 1 inherits the wildcard's repos: %v", r)
	}
	for i, tr := range got {
		if tr.Enabled == nil || !*tr.Enabled {
			t.Errorf("instance %d inherits the wildcard's enabled:", i)
		}
	}
}

// An empty array arms nothing and is a config mistake, not a silent no-op.
func TestEmptyInstanceArrayIsRejected(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", instancePack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      deploy: []
`))
	if err == nil || !strings.Contains(err.Error(), "empty array") {
		t.Fatalf("an empty arming array must be refused: %v", err)
	}
}
