package code

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/memory"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// ctx.memory.key is core's scope-key renderer, reached through the one
// ctx.memory dispatcher every engine shares (CtxHandler → memInvoke →
// memKey).

func memKeyCall(t *testing.T, h CtxHandler, args ...any) map[string]any {
	t.Helper()
	res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "key", Args: args})
	if !res.OK {
		t.Fatalf("memory.key%v: %s", args, res.Error)
	}
	m, _ := res.Value.(map[string]any)
	if _, ok := m["ok"].(bool); !ok {
		t.Fatalf("memory.key answer = %#v, want {key, ok, reason}", res.Value)
	}
	return m
}

// The shared vectors, with their facts sent the way a code step sends them —
// as JSON, so every number arrives a float64 — render exactly as core's
// RenderScopeKey renders them over a run's facts: a JSON 0 has no value, 42
// renders "42". No memory: section is configured: key reads no memory.
func TestCtxMemoryKeyMatchesTheVectors(t *testing.T) {
	memory.Reset()
	raw, err := os.ReadFile("../memory/testdata/scope_key_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Facts map[string]any `json:"facts"`
		Cases []struct {
			Template string `json:"template"`
			Want     string `json:"want"`
			Drop     bool   `json:"drop"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	h := CtxHandler{}
	for _, c := range v.Cases {
		r := memKeyCall(t, h, c.Template, v.Facts)
		switch {
		case c.Drop && (r["ok"] != false || r["key"] != "" || r["reason"] == ""):
			t.Errorf("%q must not apply: %#v", c.Template, r)
		case !c.Drop && (r["ok"] != true || r["key"] != c.Want || r["reason"] != ""):
			t.Errorf("%q = %#v, want key %q", c.Template, r, c.Want)
		}
	}
	if r := memKeyCall(t, h, "issue:{{.n}}", map[string]any{"n": float64(0)}); r["ok"] != false ||
		!strings.Contains(r["reason"].(string), "no value") {
		t.Errorf("a JSON zero is no value: %#v", r)
	}
	if r := memKeyCall(t, h, "pr:{{.n}}", map[string]any{"n": float64(1e15)}); r["key"] != "pr:1000000000000000" {
		t.Errorf("a whole JSON number renders as an int: %#v", r)
	}
	if r := memKeyCall(t, h, "acme/api"); r["key"] != "acme/api" || r["ok"] != true {
		t.Errorf("a static key needs no facts: %#v", r)
	}
	if r := memKeyCall(t, h, "x:{{.a}}", nil); r["ok"] != false {
		t.Errorf("null facts are no facts: %#v", r)
	}
}

// key touches no memory scope, so it is not put to the step's DataGuard — an
// agent-authored step's guard, which refuses an unscoped memory touch, does
// not stand between it and a template. Named secrets and vaults are never
// facts; the run's redactor scrubs tracked values; a fact's control
// character refuses the key; the caller's facts are not modified.
func TestCtxMemoryKeySafety(t *testing.T) {
	memory.Reset()
	guarded := 0
	h := CtxHandler{
		Guard: func(kind, op, resource string, args []any) error {
			guarded++
			return errRefuseAll
		},
		Redact: redactHunter2,
	}
	facts := map[string]any{"team": "pay-hunter2", "secrets": map[string]any{"gh": "tok"},
		"vaults": map[string]any{"v": map[string]any{"k": "s"}}, "nl": "a\nb", "glob": "*"}
	if r := memKeyCall(t, h, "team:{{.team}}", facts); r["key"] != "team:pay-[redacted]" {
		t.Errorf("tracked secret not redacted: %#v", r)
	}
	if facts["team"] != "pay-hunter2" {
		t.Error("the caller's facts were modified")
	}
	for _, tmpl := range []string{"s:{{.secrets.gh}}", "v:{{index .vaults.v \"k\"}}", "x:{{.nl}}", "{{if .team}}y{{end}}", "{{kv \"a\" \"b\" \"c\"}}"} {
		if r := memKeyCall(t, h, tmpl, facts); r["ok"] != false || r["key"] != "" {
			t.Errorf("%q must not apply: %#v", tmpl, r)
		}
	}
	if r := memKeyCall(t, h, "g:{{.glob}}", facts); r["key"] != "g:*" {
		t.Errorf("a fact renders as its literal text: %#v", r)
	}
	if guarded != 0 {
		t.Fatalf("key consulted the DataGuard %d times", guarded)
	}
	for _, args := range [][]any{nil, {42}, {"k", "not an object"}, {"k", map[string]any{}, "extra"}} {
		if res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "key", Args: args}); res.OK || res.Refused {
			t.Errorf("malformed call %v: %#v", args, res)
		}
	}
	// The other ops still need a memory: section and still face the guard.
	if res := h.Invoke(CtxRequest{Kind: CtxKindMemory, Op: "list"}); res.OK {
		t.Fatalf("list without memory: %#v", res)
	}
}

// redactHunter2 stands in for the run's tracked-secret redactor.
func redactHunter2(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = redactHunter2(e)
		}
	case string:
		return strings.ReplaceAll(x, "hunter2", "[redacted]")
	}
	return v
}

var errRefuseAll = &refuseAll{}

type refuseAll struct{}

func (*refuseAll) Error() string { return "agent_authored allowlist: refused" }

// An engine plugin reaches key over its host callback, carrying the step's
// redactor (Spec.Redact) — and the SDK's typed HostMemory.Key reads the
// answer.
func TestPluginEngineMemoryKey(t *testing.T) {
	memory.Reset()
	var res sdk.HostResult
	e := engineExecutor("acme-engine", fakeEngine{run: func(_ sdk.RunRequest, host func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
		res = host(sdk.HostRequest{Kind: "memory", Op: "key", Args: []any{"team:{{.team}}", map[string]any{"team": "hunter2"}}})
		return map[string]any{}, nil
	}})
	if _, err := e.Exec(context.Background(), Spec{Run: "acme-engine", Plugin: true,
		Redact: redactHunter2}, nil); err != nil {
		t.Fatal(err)
	}
	m, _ := res.Value.(map[string]any)
	if !res.OK || m["key"] != "team:[redacted]" || m["ok"] != true {
		t.Fatalf("plugin memory.key = %#v", res)
	}
}

// A `use: cli` step reaches key over its socket with the shipped client, the
// step's redactor applied.
func TestCLICtxMemoryKey(t *testing.T) {
	needSh(t)
	memory.Reset()
	helper := ctxHelperScript(t)
	out, err := (&Executor{}).Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh"}, Code: `
k=$("$HELPER" ctx memory key 'team:{{.inputs.team}}' '{"inputs":{"team":"hunter2"}}') || exit 1
d=$("$HELPER" ctx memory key 'team:{{.inputs.team}}' '{"inputs":{}}') || exit 1
printf '{"k": %s, "d": %s}' "$k" "$d"
`,
		Env:    map[string]string{"HELPER": helper, ctxClientHelperEnv: "1"},
		Redact: redactHunter2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := out["k"].(map[string]any)
	d, _ := out["d"].(map[string]any)
	if k["key"] != "team:[redacted]" || k["ok"] != true {
		t.Errorf("cli memory key = %#v", out["k"])
	}
	if d["ok"] != false || d["key"] != "" || !strings.Contains(d["reason"].(string), "no value") {
		t.Errorf("cli memory key, missing fact = %#v", out["d"])
	}
}
