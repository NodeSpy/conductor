package code

import (
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

// The ctx.memory SURFACE, through the one dispatcher every engine reaches
// it by (CtxHandler → memInvoke).

// TestCtxMemoryOpSurface: remember/recall/forget/list round-trip, and the
// entries really land in the configured memory.
func TestCtxMemoryOpSurface(t *testing.T) {
	mgr := tempMem(t)
	h := CtxHandler{}
	memCall := func(op string, args ...any) any {
		t.Helper()
		res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: op, Args: args})
		if !res.OK {
			t.Fatalf("memory.%s: %s", op, res.Error)
		}
		return res.Value
	}

	kept, _ := memCall("remember", "a note", []any{"ci"}, "repo:o/r").(map[string]any)
	if kept["scope"] != "repo:o/r" {
		t.Fatalf("remember = %#v", kept)
	}
	memCall("remember", "second", nil, "repo:o/r")

	hits, _ := memCall("recall", map[string]any{
		"tags": []any{"ci"}, "scope": "repo:o/r", "limit": 5}).([]any)
	if len(hits) != 1 {
		t.Fatalf("recall by tag = %#v", hits)
	}
	if first, _ := hits[0].(map[string]any); first["text"] != "a note" {
		t.Errorf("recalled entry = %#v", hits[0])
	}
	if all, _ := memCall("list", nil).([]any); len(all) != 2 {
		t.Fatalf("list = %#v", all)
	}
	if gone := memCall("forget", kept["id"]); gone != true {
		t.Errorf("forget = %#v", gone)
	}

	// The survivor is in the real manager, not just in the responses.
	left, _ := mgr.List()
	if len(left) != 1 || left[0].Text != "second" {
		t.Fatalf("memory after the run: %+v", left)
	}

	// Unconfigured memory says so plainly rather than nil-panicking.
	memory.Reset()
	res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "list"})
	if res.OK || !strings.Contains(res.Error, "memory: not configured") {
		t.Fatalf("unconfigured: %#v", res)
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
