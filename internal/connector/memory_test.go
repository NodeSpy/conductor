package connector

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/memory"
)

func testMemory(t *testing.T) *memory.Manager {
	t.Helper()
	memory.Reset()
	t.Cleanup(memory.Reset)
	m := memory.NewManager(memory.NewMemBackend())
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	n := 0
	m.SetClock(func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }, nil)
	memory.Configure(m)
	return m
}

// TestMemoryVerbs drives the four verbs through Invoke, with the run's
// provenance stamped on the context the way the flow runner does.
func TestMemoryVerbs(t *testing.T) {
	testMemory(t)
	impl := memoryImpl{}
	ctx := memory.WithSource(context.Background(), memory.Source{
		Agent: "fixer", Run: "flow:x:1", Trigger: "failing_checks", Repo: "acme/api",
	})

	// remember resolves relative scope against the run context.
	out, err := impl.Invoke(ctx, "remember", map[string]any{
		"text": "pin the linter version", "tags": []any{"ci", "lint"}, "scope": "repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["scope"] != "repo:acme/api" || out["id"] == "" {
		t.Fatalf("remember outputs: %v", out)
	}
	id := out["id"].(string)
	if _, err := impl.Invoke(ctx, "remember", map[string]any{"text": "global note"}); err != nil {
		t.Fatal(err)
	}

	// recall: tags + scope + limit, newest first, provenance intact.
	out, err = impl.Invoke(ctx, "recall", map[string]any{"tags": []any{"ci"}, "scope": "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if out["count"] != 1 {
		t.Fatalf("recall count: %v", out)
	}
	mem := out["memories"].([]any)[0].(map[string]any)
	if mem["text"] != "pin the linter version" {
		t.Fatalf("recall entry: %v", mem)
	}
	src := mem["source"].(map[string]any)
	if src["agent"] != "fixer" || src["run"] != "flow:x:1" || src["trigger"] != "failing_checks" || src["repo"] != "acme/api" {
		t.Fatalf("provenance not recorded: %v", src)
	}

	// list: everything, newest first.
	out, err = impl.Invoke(ctx, "list", map[string]any{})
	if err != nil || out["count"] != 2 {
		t.Fatalf("list: %v %v", err, out)
	}
	first := out["memories"].([]any)[0].(map[string]any)
	if first["text"] != "global note" {
		t.Fatalf("list order: %v", first)
	}

	// recall with substring + limit.
	out, err = impl.Invoke(ctx, "recall", map[string]any{"substring": "LINTER", "limit": 5})
	if err != nil || out["count"] != 1 {
		t.Fatalf("substring recall: %v %v", err, out)
	}

	// forget.
	out, err = impl.Invoke(ctx, "forget", map[string]any{"id": id})
	if err != nil || out["found"] != true {
		t.Fatalf("forget: %v %v", err, out)
	}
	out, err = impl.Invoke(ctx, "forget", map[string]any{"id": id})
	if err != nil || out["found"] != false {
		t.Fatalf("forget again: %v %v", err, out)
	}

	// Errors surface clearly.
	if _, err := impl.Invoke(ctx, "remember", map[string]any{"text": ""}); err == nil {
		t.Fatal("empty text should error")
	}
	if _, err := impl.Invoke(ctx, "recall", map[string]any{"scope": "bogus"}); err == nil {
		t.Fatal("bad scope should error")
	}
	if _, err := impl.Invoke(ctx, "bogus", map[string]any{}); err == nil {
		t.Fatal("unknown verb should error")
	}
}

// TestMemoryVerbsUnconfigured: without a memory: section every verb reports
// the missing config plainly.
func TestMemoryVerbsUnconfigured(t *testing.T) {
	memory.Reset()
	impl := memoryImpl{}
	for _, verb := range []string{"remember", "recall", "forget", "list"} {
		_, err := impl.Invoke(context.Background(), verb, map[string]any{"text": "x", "id": "y"})
		if err == nil || !strings.Contains(err.Error(), "memory: section") {
			t.Errorf("%s: want a not-configured error, got %v", verb, err)
		}
	}
}

// TestMemoryBuiltinRegistered: the connector is always in a built registry
// and its source face refuses triggers.
func TestMemoryBuiltinRegistered(t *testing.T) {
	reg := buildSinkRegistry(t, "connectors:\n  c: { type: command }\n")
	in, ok := reg.Get("memory")
	if !ok || in.Decl.Type != "memory" || !in.Enabled {
		t.Fatalf("memory built-in missing: %+v", in)
	}
	if _, err := in.Impl.Source([]CompiledTrigger{{}}); err == nil {
		t.Fatal("memory has no source events")
	}
	if verbs := in.Decl.VerbNames(); len(verbs) != 4 {
		t.Fatalf("verbs: %v", verbs)
	}
}

func TestStringListCoercion(t *testing.T) {
	if got := stringList([]any{"a", 1}); len(got) != 2 || got[1] != "1" {
		t.Errorf("[]any: %v", got)
	}
	if got := stringList([]string{"a"}); len(got) != 1 {
		t.Errorf("[]string: %v", got)
	}
	if got := stringList("solo"); len(got) != 1 || got[0] != "solo" {
		t.Errorf("string: %v", got)
	}
	if got := stringList(nil); got != nil {
		t.Errorf("nil: %v", got)
	}
}
