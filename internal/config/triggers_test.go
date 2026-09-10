package config

import (
	"reflect"
	"strings"
	"testing"
)

// --- the named/qualified map form -----------------------------------------

func TestTriggerMapKeyImpliesOn(t *testing.T) {
	src := `
triggers:
  github.pull_request:  { steps: [{ id: a, uses: gh.comment }] }
  gitlab.merge_request: { steps: [{ id: a, uses: gh.comment }] }
  pagerduty.incident:   { steps: [{ id: t, uses: gh.comment }] }
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 3 {
		t.Fatalf("got %d triggers", len(c.Triggers))
	}
	// Document order is preserved, and each key is both the address and the
	// implied event.
	want := []string{"github.pull_request", "gitlab.merge_request", "pagerduty.incident"}
	for i, w := range want {
		if c.Triggers[i].Name != w {
			t.Errorf("triggers[%d].Name = %q, want %q", i, c.Triggers[i].Name, w)
		}
		if c.Triggers[i].On != w {
			t.Errorf("triggers[%d].On = %q, want %q", i, c.Triggers[i].On, w)
		}
	}
}

func TestTwoTriggersOnTheSameEventUseFreeNames(t *testing.T) {
	src := `
triggers:
  review:    { on: github.pull_request, steps: [{ id: a, uses: gh.comment }] }
  autolabel: { on: github.pull_request, steps: [{ id: l, uses: gh.label }] }
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 2 {
		t.Fatalf("got %d triggers", len(c.Triggers))
	}
	for _, tr := range c.Triggers {
		if tr.On != "github.pull_request" {
			t.Errorf("%s: on = %q", tr.Name, tr.On)
		}
	}
	if c.Triggers[0].Name != "review" || c.Triggers[1].Name != "autolabel" {
		t.Errorf("names = %q, %q", c.Triggers[0].Name, c.Triggers[1].Name)
	}
}

func TestFreeNamedTriggerWithoutOnIsAnError(t *testing.T) {
	var c Config
	err := strictUnmarshal([]byte("triggers:\n  review: { steps: [] }\n"), &c)
	if err == nil || !strings.Contains(err.Error(), "needs `on:`") {
		t.Fatalf("want a needs-on: error, got %v", err)
	}
}

func TestExplicitOnBeatsTheKeyImplication(t *testing.T) {
	var c Config
	src := "triggers:\n  github.pull_request: { on: gitlab.merge_request, steps: [] }\n"
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if c.Triggers[0].On != "gitlab.merge_request" {
		t.Errorf("on = %q", c.Triggers[0].On)
	}
	if c.Triggers[0].Name != "github.pull_request" {
		t.Errorf("the key is still the address: %q", c.Triggers[0].Name)
	}
}

func TestExplicitNameBeatsTheKey(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte("triggers:\n  github.pull_request: { name: review, steps: [] }\n"), &c); err != nil {
		t.Fatal(err)
	}
	if c.Triggers[0].Name != "review" || c.Triggers[0].On != "github.pull_request" {
		t.Errorf("trigger = %#v", c.Triggers[0])
	}
}

func TestTriggerListFormStillWorks(t *testing.T) {
	src := `
triggers:
  - on: github.pull_request
    steps: [{ id: a, uses: gh.comment }]
  - on: manual
    name: sweep
    steps: [{ id: b, uses: gh.comment }]
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 2 || c.Triggers[0].On != "github.pull_request" || c.Triggers[1].Name != "sweep" {
		t.Fatalf("triggers = %#v", c.Triggers)
	}
}

func TestTriggerMapRejectsBadShapes(t *testing.T) {
	tests := map[string]string{
		"triggers: hello\n":                         "must be a list of triggers or a map",
		"triggers:\n  \"\": { on: manual }\n":       "empty trigger name",
		"triggers:\n  a: [ 3 ]\n":                   "an instance is a block",
		"triggers:\n  github.pull_request: []\n":    "instance list is empty",
		"triggers:\n  github.pull_request: hello\n": "a trigger is a block",
	}
	for src, want := range tests {
		var c Config
		err := strictUnmarshal([]byte(src), &c)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want error containing %q, got %v", src, want, err)
		}
	}
}

func TestTriggerMapIsStillStrict(t *testing.T) {
	var c Config
	err := strictUnmarshal([]byte("triggers:\n  github.pull_request: { filtres: {} }\n"), &c)
	if err == nil || !strings.Contains(err.Error(), "field filtres not found") {
		t.Fatalf("want a strict-decode error, got %v", err)
	}
}

func TestNullTriggersIsEmpty(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte("triggers:\n"), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 0 {
		t.Fatalf("triggers = %#v", c.Triggers)
	}
}

// --- array instances -------------------------------------------------------

func TestTriggerInstancesFanOut(t *testing.T) {
	src := `
triggers:
  review:
    - { on: github.pull_request, filters: { repos: [me/app] } }
    - { on: github.pull_request, filters: { repos: [me/api] } }
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 2 {
		t.Fatalf("got %d triggers", len(c.Triggers))
	}
	for _, tr := range c.Triggers {
		addr, handle := SplitInstanceName(tr.Name)
		if addr != "review" {
			t.Errorf("address = %q", addr)
		}
		if handle == "" {
			t.Errorf("%q carries no instance handle", tr.Name)
		}
		if tr.On != "github.pull_request" {
			t.Errorf("%q: on = %q", tr.Name, tr.On)
		}
	}
	if c.Triggers[0].Name == c.Triggers[1].Name {
		t.Fatal("instances must have distinct handles")
	}
}

// An instance's identity is its CONTENT, so reordering the array — or
// reordering keys within an entry — must not move its dedup/attempt state.
func TestInstanceHandlesAreStableAcrossReorder(t *testing.T) {
	a := `
triggers:
  review:
    - { on: github.pull_request, filters: { repos: [me/app] } }
    - { on: github.pull_request, filters: { repos: [me/api] } }
`
	b := `
triggers:
  review:
    - { filters: { repos: [me/api] }, on: github.pull_request }
    - { filters: { repos: [me/app] }, on: github.pull_request }
`
	names := func(src string) map[string]bool {
		var c Config
		if err := strictUnmarshal([]byte(src), &c); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, tr := range c.Triggers {
			out[tr.Name] = true
		}
		return out
	}
	if !reflect.DeepEqual(names(a), names(b)) {
		t.Fatalf("handles moved on reorder:\n%v\n%v", names(a), names(b))
	}
}

func TestIdenticalInstancesAreRejected(t *testing.T) {
	src := `
triggers:
  review:
    - { on: github.pull_request, filters: { repos: [me/app] } }
    - { on: github.pull_request, filters: { repos: [me/app] } }
`
	var c Config
	err := strictUnmarshal([]byte(src), &c)
	if err == nil || !strings.Contains(err.Error(), "identical to an earlier instance") {
		t.Fatalf("want a duplicate-instance error, got %v", err)
	}
}

func TestSingleElementInstanceListIsOneTrigger(t *testing.T) {
	var c Config
	src := "triggers:\n  github.pull_request:\n    - { filters: { repos: [me/app] } }\n"
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Triggers) != 1 {
		t.Fatalf("got %d triggers", len(c.Triggers))
	}
	if addr, _ := SplitInstanceName(c.Triggers[0].Name); addr != "github.pull_request" {
		t.Errorf("address = %q", addr)
	}
	if c.Triggers[0].On != "github.pull_request" {
		t.Errorf("on = %q", c.Triggers[0].On)
	}
}

func TestDuplicateTriggerKeyRejected(t *testing.T) {
	// yaml.v3 already rejects a duplicate mapping key; assert the config
	// surface fails loudly rather than silently keeping one.
	var c Config
	src := "triggers:\n  github.pull_request: { steps: [] }\n  github.pull_request: { steps: [] }\n"
	if err := strictUnmarshal([]byte(src), &c); err == nil {
		t.Fatal("want an error for a duplicate trigger key")
	}
}

// --- helpers ---------------------------------------------------------------

func TestIsQualifiedEvent(t *testing.T) {
	tests := map[string]bool{
		"github.pull_request": true,
		"manual":              true,
		"review":              false, // a bare word is a free name, not an event
		"gh.":                 false,
		".event":              false,
		"a.b.c":               false,
		"":                    false,
	}
	for in, want := range tests {
		if got := IsQualifiedEvent(in); got != want {
			t.Errorf("IsQualifiedEvent(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitInstanceName(t *testing.T) {
	addr, h := SplitInstanceName("review#a1b2c3d4")
	if addr != "review" || h != "a1b2c3d4" {
		t.Errorf("split = %q, %q", addr, h)
	}
	addr, h = SplitInstanceName("review")
	if addr != "review" || h != "" {
		t.Errorf("split = %q, %q", addr, h)
	}
}
