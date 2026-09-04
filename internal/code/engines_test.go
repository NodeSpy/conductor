package code

import (
	"context"
	"strings"
	"testing"
)

// The risor and lua engines share the in-process contract with js/go-embed:
// ctx in, result out, sandboxed, local-only.

func TestRisorExec(t *testing.T) {
	e := &Executor{}
	data := map[string]any{"level": "high", "items": []any{"a", "b"}, "n": 20}

	out, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `
		{"sev": ctx["level"], "count": len(ctx["items"]), "double": ctx["n"] * 2}
	`}, data)
	if err != nil {
		t.Fatal(err)
	}
	if out["sev"] != "high" {
		t.Errorf("sev: %v", out["sev"])
	}
	if toIntT(t, out["count"]) != 2 || toIntT(t, out["double"]) != 40 {
		t.Errorf("count/double: %v %v", out["count"], out["double"])
	}

	// A non-map result lands under value:.
	out, err = e.Exec(context.Background(), Spec{Run: "risor", Code: `1 + 2`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if toIntT(t, out["value"]) != 3 {
		t.Errorf("scalar result: %v", out)
	}

	// A script error surfaces with the engine named.
	if _, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `nope(`}, nil); err == nil || !strings.Contains(err.Error(), "risor") {
		t.Fatalf("want a risor error, got %v", err)
	}

	// Sandbox: no os/exec built-ins are exposed.
	if _, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `os.getenv("HOME")`}, nil); err == nil {
		t.Fatal("risor must not expose an os module")
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `exec("id")`}, nil); err == nil {
		t.Fatal("risor must not expose exec")
	}
}

func TestLuaExec(t *testing.T) {
	e := &Executor{}
	data := map[string]any{
		"level": "high",
		"items": []any{"a", "b", "c"},
		"inner": map[string]any{"n": 21},
	}
	out, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `
		return { sev = ctx.level, count = #ctx.items, double = ctx.inner.n * 2 }
	`}, data)
	if err != nil {
		t.Fatal(err)
	}
	if out["sev"] != "high" {
		t.Errorf("sev: %v", out["sev"])
	}
	if toIntT(t, out["count"]) != 3 || toIntT(t, out["double"]) != 42 {
		t.Errorf("count/double: %v %v", out["count"], out["double"])
	}

	// A returned array becomes a value: list; a scalar lands under value:.
	out, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `return {"x", "y"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := out["value"].([]any); !ok || len(l) != 2 || l[0] != "x" {
		t.Errorf("list result: %#v", out)
	}
	out, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `return "plain"`}, nil)
	if err != nil || out["value"] != "plain" {
		t.Errorf("scalar result: %v %v", out, err)
	}

	// No return → empty outputs.
	out, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `local x = 1`}, nil)
	if err != nil || len(out) != 0 {
		t.Errorf("no-return: %v %v", out, err)
	}

	// A script error surfaces with the engine named.
	if _, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `return nope(`}, nil); err == nil || !strings.Contains(err.Error(), "lua") {
		t.Fatalf("want a lua error, got %v", err)
	}
}

// TestLuaSandbox: os/io/debug are never opened, and the base library's
// file/chunk loaders are removed.
func TestLuaSandbox(t *testing.T) {
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `
		return {
			os_nil = os == nil,
			io_nil = io == nil,
			debug_nil = debug == nil,
			dofile_nil = dofile == nil,
			loadfile_nil = loadfile == nil,
			load_nil = load == nil,
			loadstring_nil = loadstring == nil,
			string_ok = string.upper("a") == "A",
			math_ok = math.floor(1.9) == 1,
		}
	`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range out {
		if v != true {
			t.Errorf("%s = %v, want true", k, v)
		}
	}
}

func toIntT(t *testing.T, v any) int {
	t.Helper()
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	t.Fatalf("not a number: %T %v", v, v)
	return 0
}
