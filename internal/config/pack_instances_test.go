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
        - { enabled: true, repos: [team-b/*], filter: { label_any: [urgent] } }
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
			t.Errorf("instance %d carries no internal instance key: %q", i, tr.Name)
		}
	}
	// Each instance carries its OWN arming.
	if r := repoList(got[0]); len(r) != 1 || r[0] != "team-a/*" {
		t.Errorf("instance 0 repos: %v", r)
	}
	if r := repoList(got[1]); len(r) != 1 || r[0] != "team-b/*" {
		t.Errorf("instance 1 repos: %v", r)
	}
	if hasMatchKey(got[0].Filter, "label_any") {
		t.Error("instance 0 must not see instance 1's filter — a shallow copy " +
			"would have them sharing the node")
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
	// The diagnostic points at the entry by its POSITION in the operator's own
	// file. That is a pointer, not an address — there is no syntax for naming
	// an instance, and the internal content key must never leak into it.
	if err != nil && !strings.Contains(err.Error(), "instance 2 of 2") {
		t.Errorf("the error should point at WHICH entry: %v", err)
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
        - { filter: { label_any: [urgent] } }
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

// THE POINT OF CONTENT-KEYING: reordering the array is a NO-OP for state.
//
// Each instance's internal key is derived from its own content, so an operator
// who moves an entry up or down keeps that entry's dedup, session and outcome
// history. Under index-keyed identity a reorder silently re-points every
// instance's state at a different arming — the kind of breakage that shows up
// as "why did it re-run everything" days later.
func TestReorderingTheInstanceArrayKeepsEachKeyStable(t *testing.T) {
	first := instancesOf(t, `
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true, repos: [team-b/*], filter: { label_any: [urgent] } }
`)
	// The same two armings, written in the other order.
	second := instancesOf(t, `
    triggers:
      deploy:
        - { enabled: true, repos: [team-b/*], filter: { label_any: [urgent] } }
        - { enabled: true, repos: [team-a/*] }
`)
	keyOf := func(trs []TriggerSpec, repo string) string {
		t.Helper()
		for _, tr := range trs {
			if r := repoList(tr); len(r) == 1 && r[0] == repo {
				return tr.Name
			}
		}
		t.Fatalf("no instance for %q", repo)
		return ""
	}
	for _, repo := range []string{"team-a/*", "team-b/*"} {
		before, after := keyOf(first, repo), keyOf(second, repo)
		if before != after {
			t.Errorf("the %s instance changed key across a reorder (%q -> %q) — its "+
				"dedup/session/outcome state would be orphaned. Identity must be "+
				"CONTENT, not position.", repo, before, after)
		}
	}
}

// …and an EDIT to an instance is a new instance, which is the same rule read
// the other way: the state belongs to the arming, not to the slot.
func TestEditingAnInstanceChangesItsKey(t *testing.T) {
	a := instancesOf(t, `
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true, repos: [team-b/*] }
`)
	b := instancesOf(t, `
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true, repos: [team-c/*] }
`)
	if a[0].Name != b[0].Name {
		t.Errorf("the untouched instance must keep its key: %q vs %q", a[0].Name, b[0].Name)
	}
	if a[1].Name == b[1].Name {
		t.Error("an edited instance is a different arming and must get its own key")
	}
}

// Two byte-identical entries collapse to one content key, so they are one
// arming written twice — a config mistake, not a silent deduplication.
func TestIdenticalInstancesAreALoadError(t *testing.T) {
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
        - { enabled: true, repos: [team-a/*] }
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate trigger instance") {
		t.Fatalf("two identical arm entries must be a load error: %v", err)
	}
	// Key order within an entry is not content: these are still identical.
	dir2 := t.TempDir()
	writePackSource(t, dir2, "src/review", instancePack)
	_, err = resolveAndLoad(t, writeDoc(t, dir2, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { repos: [team-a/*], enabled: true }
`))
	if err == nil || !strings.Contains(err.Error(), "duplicate trigger instance") {
		t.Fatalf("key order within an entry is not content — still a duplicate: %v", err)
	}
}

// NO USER-FACING INSTANCE ID. The internal content key must not appear in any
// config surface: there is no `deploy#<hash>` or `deploy[1]` to write, and an
// `on:` overlay addresses the trigger by the name the author wrote, applying
// to every instance of it.
func TestThereIsNoWayToAddressOneInstance(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", instancePack)
	for _, attempt := range []string{"deploy[1]", "deploy#0", "deploy#abcd1234"} {
		_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    on:
      "`+attempt+`": { filter: { label_any: [x] } }
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true, repos: [team-b/*] }
`))
		if err == nil || !strings.Contains(err.Error(), "addresses no trigger") {
			t.Errorf("%q must not address an instance — instances are array entries, "+
				"edited where they sit: %v", attempt, err)
		}
	}
	// The trigger's own name still overlays ALL of its instances.
	cfg := instancesOf(t, `
    on:
      deploy: { filter: { label_any: [everywhere] } }
    triggers:
      deploy:
        - { enabled: true, repos: [team-a/*] }
        - { enabled: true, repos: [team-b/*] }
`)
	for i, tr := range cfg {
		l := tr.Filter.MatchUnion("label_any")
		if len(l) != 1 || l[0] != "everywhere" {
			t.Errorf("instance %d did not receive the trigger-wide overlay: %v", i, tr.Filter)
		}
	}
}
