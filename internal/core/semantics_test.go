package core

import "testing"

// TestLookupFactExactKeyWinsOverDottedPath is a test gap from finding 4(d):
// LookupFact's doc comment promises "a flat key of that exact name wins"
// over walking the dotted path through nested maps — e.g. a fact literally
// named "chat.user" (some plugin's own flat-key convention) must be read
// directly, not shadowed by a coincidentally-nested facts["chat"]["user"].
func TestLookupFactExactKeyWinsOverDottedPath(t *testing.T) {
	facts := map[string]any{
		"chat.user": "flat-value",
		"chat":      map[string]any{"user": "nested-value"},
	}
	got, ok := LookupFact(facts, "chat.user")
	if !ok {
		t.Fatal("LookupFact(\"chat.user\") not found")
	}
	if got != "flat-value" {
		t.Fatalf("LookupFact(\"chat.user\") = %v, want the exact flat key \"flat-value\", not the nested path's %q", got, "nested-value")
	}
}

// TestLookupFactFallsBackToDottedPath proves the companion behavior: absent
// an exact flat key, the dotted path through nested maps is still walked.
func TestLookupFactFallsBackToDottedPath(t *testing.T) {
	facts := map[string]any{
		"chat": map[string]any{"user": "nested-value"},
	}
	got, ok := LookupFact(facts, "chat.user")
	if !ok || got != "nested-value" {
		t.Fatalf("LookupFact(\"chat.user\") = (%v, %v), want (\"nested-value\", true)", got, ok)
	}
}
