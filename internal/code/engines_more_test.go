package code

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hosts"
)

// TestExecJSErrors: a syntax error, a runtime throw, and unmarshalable ctx
// each surface as clear js errors, not panics.
func TestExecJSErrors(t *testing.T) {
	e := &Executor{}
	if _, err := e.Exec(context.Background(), Spec{Run: "js", Code: "return {"}, nil); err == nil || !strings.Contains(err.Error(), "code: js") {
		t.Fatalf("syntax error: %v", err)
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "js", Code: `throw new Error("boom")`}, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("runtime throw: %v", err)
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "js", Code: "return 1"},
		map[string]any{"ch": make(chan int)}); err == nil || !strings.Contains(err.Error(), "marshal ctx") {
		t.Fatalf("marshal ctx: %v", err)
	}
}

// TestGoEmbedSignatureContract: every rejected `run` shape names the
// contract instead of panicking in reflect.
func TestGoEmbedSignatureContract(t *testing.T) {
	e := &Executor{}
	cases := []struct{ name, code string }{
		{"not a func", "var run = 3"},
		{"no args", "func run() any { return nil }"},
		{"wrong arg type", "func run(n int) any { return n }"},
		{"no returns", `func run(ctx map[string]any) {}`},
		{"bad second return", `func run(ctx map[string]any) (any, int) { return nil, 0 }`},
		{"three returns", `func run(ctx map[string]any) (any, any, error) { return nil, nil, nil }`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: c.code}, nil)
			if err == nil || !strings.Contains(err.Error(), "must define") {
				t.Fatalf("want contract error, got %v", err)
			}
		})
	}
	// The (any, error) shape's error return propagates.
	_, err := e.Exec(context.Background(), Spec{Run: "go-embed",
		Code: `import "errors"
func run(ctx map[string]any) (any, error) { return nil, errors.New("nope") }`}, nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("run error must propagate: %v", err)
	}
}

// TestExecGoToolchainErrors: a missing toolchain names the go-embed
// fallback; a compile failure carries the compiler's stderr.
func TestExecGoToolchainErrors(t *testing.T) {
	missing := &Executor{LookPath: func(string) (string, error) { return "", fmt.Errorf("nope") }}
	_, err := missing.Exec(context.Background(), Spec{Run: "go", Code: "package main"}, nil)
	if err == nil || !strings.Contains(err.Error(), "go-embed") {
		t.Fatalf("missing toolchain must name the fallback: %v", err)
	}

	e := &Executor{}
	if _, lerr := e.lookPath()("go"); lerr != nil {
		t.Skip("no go on PATH")
	}
	_, err = e.Exec(context.Background(), Spec{Run: "go", Code: "package main\nfunc main() { undefined() }"}, nil)
	if err == nil || !strings.Contains(err.Error(), "code: go: run") {
		t.Fatalf("compile failure: %v", err)
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "go", Code: "package main"},
		map[string]any{"ch": make(chan int)}); err == nil || !strings.Contains(err.Error(), "marshal ctx") {
		t.Fatalf("marshal ctx: %v", err)
	}
}

// TestLuaValueConversions: the full JSON-shaped type set crosses into Lua
// and back — including Go ints/[]string from rendered templates, mixed
// tables, floats, and nil.
func TestLuaValueConversions(t *testing.T) {
	e := &Executor{}
	data := map[string]any{
		"b":     true,
		"s":     "str",
		"i":     int(7),
		"i64":   int64(8),
		"f":     float64(1.5),
		"list":  []any{"a", float64(2)},
		"strs":  []string{"x", "y"},
		"m":     map[string]any{"k": "v"},
		"empty": nil,
		"other": struct{ A int }{A: 1}, // unsupported → stringified
	}
	out, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `
return {
  b = ctx.b, s = ctx.s, i = ctx.i, i64 = ctx.i64, f = ctx.f,
  first = ctx.list[1], second = ctx.list[2],
  sx = ctx.strs[1], k = ctx.m.k,
  isnil = (ctx.empty == nil),
  other = ctx.other,
}`}, data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"b": true, "s": "str", "i": int64(7), "i64": int64(8), "f": 1.5,
		"first": "a", "second": int64(2), "sx": "x", "k": "v",
		"isnil": true, "other": "{1}",
	}
	for k, w := range want {
		if out[k] != w {
			t.Errorf("%s = %#v, want %#v", k, out[k], w)
		}
	}

	// A pure array table returns a list; a mixed table becomes a map keeping
	// numeric entries under stringified keys; nil return means no outputs.
	out, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `return {"a", "b"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lst, ok := out["value"].([]any); !ok || len(lst) != 2 || lst[0] != "a" {
		t.Fatalf("array table: %#v", out)
	}
	out, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `return {"a", x = "y"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["x"] != "y" || out["1"] != "a" {
		t.Fatalf("mixed table: %#v", out)
	}
	out, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `return nil`}, nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("nil return: %#v %v", out, err)
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `return {`}, nil); err == nil || !strings.Contains(err.Error(), "lua") {
		t.Fatalf("lua syntax error: %v", err)
	}
}

// TestExecRisorError: a bad script is a risor error, not a panic.
func TestExecRisorError(t *testing.T) {
	e := &Executor{}
	if _, err := e.Exec(context.Background(), Spec{Run: "risor", Code: "]["}, nil); err == nil || !strings.Contains(err.Error(), "risor") {
		t.Fatalf("risor error: %v", err)
	}
}

// TestExecHostLocalErrors: a failing script carries its stderr; an
// unmarshalable ctx errors before anything runs.
func TestExecHostLocalErrors(t *testing.T) {
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{Run: "sh", Code: "echo doomed >&2; exit 3"}, nil)
	if err == nil || !strings.Contains(err.Error(), "doomed") {
		t.Fatalf("script failure stderr: %v", err)
	}
	if _, err := e.Exec(context.Background(), Spec{Run: "sh", Code: "true"},
		map[string]any{"ch": make(chan int)}); err == nil || !strings.Contains(err.Error(), "marshal ctx") {
		t.Fatalf("marshal ctx: %v", err)
	}
}

// TestExecRemoteFailureTail: a non-zero remote exit reports the LAST lines
// of stderr (a runaway script's output is truncated to its tail).
func TestExecRemoteFailureTail(t *testing.T) {
	e := &Executor{SSH: localSSH(t)}
	tgt := &hosts.Target{Name: "box", Cfg: config.HostConfig{Host: "unused"}}

	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "echo line-%d >&2\n", i)
	}
	b.WriteString("exit 3")
	_, err := e.Exec(context.Background(), Spec{Run: "sh", Code: b.String(), Host: tgt}, nil)
	if err == nil || !strings.Contains(err.Error(), "exit 3") {
		t.Fatalf("remote failure: %v", err)
	}
	if !strings.Contains(err.Error(), "line-30") || strings.Contains(err.Error(), "line-1\n") {
		t.Fatalf("stderr must be tail-truncated: %v", err)
	}

	// Short stderr passes through untruncated.
	_, err = e.Exec(context.Background(), Spec{Run: "sh", Code: "echo brief >&2; exit 1", Host: tgt}, nil)
	if err == nil || !strings.Contains(err.Error(), "brief") {
		t.Fatalf("short stderr: %v", err)
	}

	// An unnamed inline ssh: target is labeled by its address.
	anon := &hosts.Target{Cfg: config.HostConfig{Host: "10.9.8.7"}}
	_, err = e.Exec(context.Background(), Spec{Run: "definitely-absent-xyz", Code: "x", Host: anon}, nil)
	if err == nil || !strings.Contains(err.Error(), "10.9.8.7") {
		t.Fatalf("host label fallback: %v", err)
	}

	// Unmarshalable ctx errors before the SSH hop.
	if _, err := e.Exec(context.Background(), Spec{Run: "sh", Code: "true", Host: tgt},
		map[string]any{"ch": make(chan int)}); err == nil || !strings.Contains(err.Error(), "marshal ctx") {
		t.Fatalf("marshal ctx: %v", err)
	}
}

// TestRemoteScriptGoForm: run: go ships main.go and execs `go run` on the
// remote — the script builder's go-specific shape.
func TestRemoteScriptGoForm(t *testing.T) {
	s := remoteScript("go", "package main", "box", []string{"-v"})
	for _, want := range []string{`mv "$t/code" "$t/main.go"`, `exec go run "$t/main.go"`, "'-v'"} {
		if !strings.Contains(s, want) {
			t.Fatalf("script missing %q:\n%s", want, s)
		}
	}
	s = remoteScript("ruby", "puts 1", "box", nil)
	if !strings.Contains(s, `exec 'ruby' "$t/code"`) {
		t.Fatalf("interpreter form:\n%s", s)
	}
}
