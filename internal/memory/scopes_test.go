package memory

import (
	"reflect"
	"strings"
	"testing"
)

// The memory core knows nothing about repos or agents any more
// (docs/design/agents-removal.md §2). A memory is GLOBAL (no key) or filed
// under an arbitrary opaque string. These tests pin that there is no
// privileged scope TYPE left anywhere in the surface.

func TestNoPrivilegedScopeTypes(t *testing.T) {
	m := testManager(t, NewMemBackend())
	src := Source{Step: "reviewer", Repo: "acme/api"}

	// The strings that used to be TYPES are now ordinary keys: "repo" files
	// under "repo", not under the source's repo.
	e, err := m.Remember("x", nil, "repo", src)
	if err != nil {
		t.Fatal(err)
	}
	if e.Scope != "repo" {
		t.Fatalf("%q must be stored verbatim, got %q", "repo", e.Scope)
	}
	e, err = m.Remember("y", nil, "agent", src)
	if err != nil {
		t.Fatal(err)
	}
	if e.Scope != "agent" {
		t.Fatalf("%q must be stored verbatim, got %q", "agent", e.Scope)
	}
	// And the source is NOT consulted to expand anything.
	if got, _ := m.Recall(Query{Scopes: []string{"acme/api"}}); len(got) != 0 {
		t.Fatalf("nothing should have been filed under the source repo, got %+v", got)
	}
}

func TestGlobalIsTheAbsenceOfAKey(t *testing.T) {
	m := testManager(t, NewMemBackend())
	if _, err := m.Remember("shared", nil, "", Source{}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Recall(Query{Scopes: []string{""}})
	if err != nil {
		t.Fatal(err)
	}
	// An empty query scope is dropped (it narrows nothing), so this returns
	// everything — including the shared entry.
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	if got[0].Scope != GlobalScope {
		t.Fatalf("the shared set persists as %q, got %q", GlobalScope, got[0].Scope)
	}
}

func TestArbitraryKeysRoundTrip(t *testing.T) {
	m := testManager(t, NewMemBackend())
	keys := []string{
		"acme/api",                     // a repo string, by convention
		"nightly-audit",                // a workflow name
		"github.pull_request/security", // a step identity
		"agent:fixer",                  // the pre-removal alias
		"a key with spaces",
		"🙂",
	}
	for _, k := range keys {
		if _, err := m.Remember("note for "+k, nil, k, Source{}); err != nil {
			t.Fatalf("key %q: %v", k, err)
		}
	}
	for _, k := range keys {
		got, err := m.Recall(Query{Scopes: []string{k}})
		if err != nil || len(got) != 1 || got[0].Text != "note for "+k {
			t.Fatalf("recall by %q: %v %+v", k, err, got)
		}
	}
}

// --- the engine's context-key CONVENTION ----------------------------------

func TestContextKeys(t *testing.T) {
	got := ContextKeys("acme/api", "nightly", "review")
	want := []string{GlobalScope, "acme/api", "nightly", "review", "agent:review"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("context keys = %v, want %v", got, want)
	}
	// Empty components are skipped rather than becoming keys that match
	// nothing.
	if got := ContextKeys("", "", ""); !reflect.DeepEqual(got, []string{GlobalScope}) {
		t.Fatalf("empty context = %v, want just the shared set", got)
	}
	if got := ContextKeys("acme/api", "", ""); !reflect.DeepEqual(got, []string{GlobalScope, "acme/api"}) {
		t.Fatalf("repo-only context = %v", got)
	}
}

// The pre-removal alias keeps an upgraded box's accumulated memories
// readable: they were written under "agent:<name>", and a migrated step
// carries that name as its identity.
func TestLegacyStepScopeAliasIsRecalled(t *testing.T) {
	m := testManager(t, NewMemBackend())
	if _, err := m.Remember("learned before the upgrade", nil, "agent:fixer", Source{}); err != nil {
		t.Fatal(err)
	}
	section := m.PromptSection(Filter{}, ContextKeys("acme/api", "nightly", "fixer"))
	if !strings.Contains(section, "learned before the upgrade") {
		t.Fatalf("a pre-removal memory must still be recalled:\n%s", section)
	}
	if LegacyStepScope("") != "" {
		t.Fatal("no identity means no alias")
	}
}

func TestExpandScopeRefs(t *testing.T) {
	got := ExpandScopeRefs([]string{"${repo}", "literal", "${workflow}", "${step}"}, "acme/api", "nightly", "review")
	want := []string{"acme/api", "literal", "nightly", "review"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded = %v, want %v", got, want)
	}
	// A placeholder whose referent is empty drops rather than becoming "".
	got = ExpandScopeRefs([]string{"${repo}", "literal"}, "", "", "")
	if !reflect.DeepEqual(got, []string{"literal"}) {
		t.Fatalf("unresolvable placeholder should drop, got %v", got)
	}
	if ExpandScopeRefs(nil, "r", "w", "s") != nil {
		t.Fatal("no scopes means no expansion")
	}
}

// A filter's explicit scopes are used verbatim — the context keys are only
// the DEFAULT.
func TestFilterScopesOverrideContextKeys(t *testing.T) {
	m := testManager(t, NewMemBackend())
	if _, err := m.Remember("in context", nil, "acme/api", Source{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remember("out of context", nil, "elsewhere", Source{}); err != nil {
		t.Fatal(err)
	}
	keys := ContextKeys("acme/api", "", "review")
	if s := m.PromptSection(Filter{}, keys); !strings.Contains(s, "in context") || strings.Contains(s, "out of context") {
		t.Fatalf("default section should be the context keys:\n%s", s)
	}
	s := m.PromptSection(Filter{Scopes: []string{"elsewhere"}}, keys)
	if !strings.Contains(s, "out of context") || strings.Contains(s, "in context") {
		t.Fatalf("an explicit filter must override the context keys:\n%s", s)
	}
}
