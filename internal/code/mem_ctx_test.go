package code

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/memory"
)

// tempMem configures an ephemeral memory manager for a code-binding test.
func tempMem(t *testing.T) *memory.Manager {
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

// TestCtxMemoryJS: the full ctx.memory surface from run: js.
func TestCtxMemoryJS(t *testing.T) {
	mgr := tempMem(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `
const kept = ctx.memory.remember("js note", ["ci"], "repo:o/r");
ctx.memory.remember("second");
const hits = ctx.memory.recall({ tags: ["ci"], scope: "repo:o/r", limit: 5 });
const all = ctx.memory.list();
const gone = ctx.memory.forget(kept.id);
return { id: kept.id, scope: kept.scope, hits: hits.length, text: hits[0].text,
         all: all.length, gone: gone, left: ctx.memory.list().length };`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["scope"] != "repo:o/r" || out["hits"] != float64(1) || out["text"] != "js note" ||
		out["all"] != float64(2) || out["gone"] != true || out["left"] != float64(1) {
		t.Fatalf("js ctx.memory: %+v", out)
	}
	if all, _ := mgr.List(); len(all) != 1 || all[0].Text != "second" {
		t.Fatalf("store after js run: %+v", all)
	}

	// Unconfigured memory throws a clear error.
	memory.Reset()
	_, err = e.Exec(context.Background(), Spec{Run: "js", Code: `ctx.memory.list(); return 1;`}, nil)
	if err == nil || !strings.Contains(err.Error(), "memory: not configured") {
		t.Fatalf("unconfigured: %v", err)
	}
}

// TestCtxMemoryRisor: the top-level memory module from run: risor.
func TestCtxMemoryRisor(t *testing.T) {
	tempMem(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `
kept := memory.remember("risor note", ["infra"], "acme/infra")
hits := memory.recall({"tags": ["infra"]})
{"scope": kept["scope"], "n": len(hits), "text": hits[0]["text"], "gone": memory.forget(kept["id"])}
`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["scope"] != "acme/infra" || out["text"] != "risor note" || out["gone"] != true {
		t.Fatalf("risor memory: %+v", out)
	}
}

// TestCtxMemoryLua: ctx.memory from run: lua.
func TestCtxMemoryLua(t *testing.T) {
	tempMem(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `
local kept = ctx.memory.remember("lua note", {"db"}, "agent:bot")
local hits = ctx.memory.recall({ tags = {"db"}, scope = "agent:bot" })
return { scope = kept.scope, n = #hits, text = hits[1].text, gone = ctx.memory.forget(kept.id) }
`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["scope"] != "agent:bot" || out["text"] != "lua note" || out["gone"] != true {
		t.Fatalf("lua memory: %+v", out)
	}
}

// TestCtxMemoryGoEmbed: import "conductor/memory" from run: go-embed.
func TestCtxMemoryGoEmbed(t *testing.T) {
	tempMem(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: `
import "conductor/memory"

func run(ctx map[string]any) (any, error) {
	kept, err := memory.Remember("go note", []string{"style"}, "")
	if err != nil {
		return nil, err
	}
	hits, err := memory.Recall(map[string]any{"tags": []string{"style"}})
	if err != nil {
		return nil, err
	}
	gone, err := memory.Forget(kept["id"].(string))
	if err != nil {
		return nil, err
	}
	left, err := memory.List()
	if err != nil {
		return nil, err
	}
	first := hits[0].(map[string]any)
	return map[string]any{"scope": kept["scope"], "text": first["text"], "gone": gone, "left": len(left)}, nil
}
`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["scope"] != "global" || out["text"] != "go note" || out["gone"] != true || out["left"] != 0 {
		t.Fatalf("go-embed memory: %+v", out)
	}
}

// TestMemInvokeValidation: the shared dispatcher's argument contract.
func TestMemInvokeValidation(t *testing.T) {
	tempMem(t)
	if _, err := memInvoke(nil, "remember", nil); err == nil {
		t.Error("remember without text must error")
	}
	if _, err := memInvoke(nil, "remember", []any{"x", 42, ""}); err == nil {
		t.Error("non-list tags must error")
	}
	if _, err := memInvoke(nil, "recall", []any{"not a map"}); err == nil {
		t.Error("non-map recall options must error")
	}
	// A scope key is opaque — there is no relative form to fail on any more
	// (design §2), so this is simply a recall against the key "repo".
	if _, err := memInvoke(nil, "recall", []any{map[string]any{"scope": "repo"}}); err != nil {
		t.Errorf("an opaque scope key must be accepted: %v", err)
	}
	if _, err := memInvoke(nil, "forget", nil); err == nil {
		t.Error("forget without id must error")
	}
	if _, err := memInvoke(nil, "bogus", nil); err == nil {
		t.Error("unknown op must error")
	}
}
