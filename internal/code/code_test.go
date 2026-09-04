package code

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/hosts"
)

// ---- run: js ---------------------------------------------------------------

func TestExecJS_ReturnsObject(t *testing.T) {
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `return {a: 1, b: "two"};`}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	want := map[string]any{"a": float64(1), "b": "two"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %#v, want %#v", out, want)
	}
}

func TestExecJS_ReturnsScalar(t *testing.T) {
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `return 42;`}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	want := map[string]any{"value": float64(42)}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %#v, want %#v", out, want)
	}
}

func TestExecJS_ReadsCtx(t *testing.T) {
	e := &Executor{}
	data := map[string]any{"a": map[string]any{"b": 2}}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `return {sum: ctx.a.b + 1};`}, data)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	want := map[string]any{"sum": float64(3)}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %#v, want %#v", out, want)
	}
}

func TestExecJS_SyntaxError(t *testing.T) {
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{Run: "js", Code: `this is not valid js {{{`}, nil)
	if err == nil {
		t.Fatal("expected a syntax error")
	}
}

func TestExecJS_NoReturn(t *testing.T) {
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `let x = 1;`}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("out = %#v, want empty", out)
	}
}

// ---- run: go-embed ----------------------------------------------------------

func TestExecGoEmbed_AnySignature(t *testing.T) {
	e := &Executor{}
	code := `
import "strings"

func run(ctx map[string]any) any {
	return map[string]any{"upper": strings.ToUpper(ctx["name"].(string))}
}
`
	out, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: code}, map[string]any{"name": "alice"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	want := map[string]any{"upper": "ALICE"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %#v, want %#v", out, want)
	}
}

func TestExecGoEmbed_AnyErrorSignature_Success(t *testing.T) {
	e := &Executor{}
	code := `
import "encoding/json"

func run(ctx map[string]any) (any, error) {
	b, err := json.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"json": string(b)}, nil
}
`
	out, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: code}, map[string]any{"x": float64(1)})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out["json"] != `{"x":1}` {
		t.Errorf("out = %#v", out)
	}
}

func TestExecGoEmbed_ErrorBranchPropagates(t *testing.T) {
	e := &Executor{}
	code := `
import "errors"

func run(ctx map[string]any) (any, error) {
	return nil, errors.New("boom")
}
`
	_, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: code}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error containing %q, got %v", "boom", err)
	}
}

func TestExecGoEmbed_SandboxBlocksOS(t *testing.T) {
	e := &Executor{}
	code := `
import "os"

func run(ctx map[string]any) any {
	return os.Getenv("HOME")
}
`
	_, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: code}, nil)
	if err == nil {
		t.Fatal("expected importing \"os\" to fail (sandboxed)")
	}
}

func TestExecGoEmbed_MissingRun(t *testing.T) {
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: `var x = 1`}, nil)
	if err == nil || !strings.Contains(err.Error(), goEmbedContractMsg) {
		t.Fatalf("expected contract error, got %v", err)
	}
}

func TestExecGoEmbed_WrongSignature(t *testing.T) {
	e := &Executor{}
	code := `
func run(ctx map[string]any) string {
	return "nope"
}
`
	_, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: code}, nil)
	if err == nil || !strings.Contains(err.Error(), goEmbedContractMsg) {
		t.Fatalf("expected contract error, got %v", err)
	}
}

// ---- run: go -----------------------------------------------------------------

const goProgram = `
package main

import (
	"encoding/json"
	"os"
)

func main() {
	var ctx struct {
		N float64 ` + "`json:\"n\"`" + `
	}
	if err := json.NewDecoder(os.Stdin).Decode(&ctx); err != nil {
		panic(err)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"n": ctx.N * 2})
}
`

func TestExecGo_EchoesDoubled(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "go", Code: goProgram}, map[string]any{"n": float64(21)})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out["n"] != float64(42) {
		t.Errorf("out = %#v", out)
	}
}

func TestExecGo_NotOnPath(t *testing.T) {
	e := &Executor{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	_, err := e.Exec(context.Background(), Spec{Run: "go", Code: goProgram}, nil)
	if err == nil || !strings.Contains(err.Error(), "go not found on PATH") {
		t.Fatalf("expected 'go not found' error, got %v", err)
	}
}

// ---- host interpreters (local) -----------------------------------------------

func TestExecHostLocal_StdinStdoutJSON(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	e := &Executor{}
	script := `read -r line; echo "{\"got\": $line}"`
	out, err := e.Exec(context.Background(), Spec{Run: "sh", Code: script}, map[string]any{"n": float64(7)})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// stdin is the ctx JSON on one line; embed it back inside the object.
	if out["got"] == nil {
		t.Fatalf("out = %#v", out)
	}
}

func TestExecHostLocal_NonJSONStdout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "sh", Code: `echo hello there`}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	want := map[string]any{"text": "hello there"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %#v, want %#v", out, want)
	}
}

func TestExecHostLocal_NonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{Run: "sh", Code: `echo boom-message >&2; exit 5`}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom-message") {
		t.Fatalf("expected error containing stderr, got %v", err)
	}
}

func TestExecHostLocal_ArgsPassthrough(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run:  "sh",
		Code: `echo "{\"arg1\": \"$1\", \"arg2\": \"$2\"}"`,
		Args: []string{"hello", "world"},
	}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	want := map[string]any{"arg1": "hello", "arg2": "world"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("out = %#v, want %#v", out, want)
	}
}

func TestExecHostLocal_WorkDirHonored(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "sh", Code: `echo "{\"pwd\": \"$(pwd)\"}"`, WorkDir: "/tmp"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out["pwd"] != "/tmp" {
		t.Errorf("out = %#v", out)
	}
}

func TestExecHostLocal_EnvHonored(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run:  "sh",
		Code: `echo "{\"greeting\": \"$GREETING\"}"`,
		Env:  map[string]string{"GREETING": "hello there"},
	}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out["greeting"] != "hello there" {
		t.Errorf("out = %#v", out)
	}
}

func TestExecHostLocal_InterpreterNotFound(t *testing.T) {
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{Run: "definitely-absent-xyz", Code: "irrelevant"}, nil)
	if err == nil {
		t.Fatal("expected error for missing interpreter")
	}
}

// ---- remote execution ---------------------------------------------------------

// localSSH builds a *hosts.Client whose Run fakes SSH by executing the
// trailing remote-command argv element through a local shell — the same
// trick internal/hosts's own local-integration test uses, letting the
// remote-script builder (base64 framing, command -v check, exec …) be
// proven end-to-end without a real network hop.
func localSSH(t *testing.T) *hosts.Client {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	return &hosts.Client{Run: func(ctx context.Context, argv []string, stdin []byte) (string, string, int, error) {
		remote := argv[len(argv)-1]
		cmd := exec.CommandContext(ctx, "sh", "-c", remote)
		cmd.Stdin = strings.NewReader(string(stdin))
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
				err = nil
			}
		}
		return stdout.String(), stderr.String(), exitCode, err
	}}
}

func TestExecRemote_StdinAndBase64Framing(t *testing.T) {
	e := &Executor{SSH: localSSH(t)}
	tgt := &hosts.Target{Name: "box", Cfg: config.HostConfig{Host: "unused"}}
	script := `read -r line; echo "{\"got\": $line}"`
	out, err := e.Exec(context.Background(), Spec{Run: "sh", Code: script, Host: tgt}, map[string]any{"n": float64(9)})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out["got"] == nil {
		t.Fatalf("out = %#v", out)
	}
}

func TestExecRemote_ArgsQuoted(t *testing.T) {
	e := &Executor{SSH: localSSH(t)}
	tgt := &hosts.Target{Name: "box", Cfg: config.HostConfig{Host: "unused"}}
	out, err := e.Exec(context.Background(), Spec{
		Run:  "sh",
		Code: `echo "{\"a\": \"$1\"}"`,
		Args: []string{"a 'b' $c"},
		Host: tgt,
	}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out["a"] != "a 'b' $c" {
		t.Errorf("out = %#v", out)
	}
}

func TestExecRemote_InterpreterNotFound(t *testing.T) {
	e := &Executor{SSH: localSSH(t)}
	tgt := &hosts.Target{Name: "prod-box", Cfg: config.HostConfig{Host: "unused"}}
	_, err := e.Exec(context.Background(), Spec{Run: "definitely-absent-xyz", Code: "irrelevant", Host: tgt}, nil)
	if err == nil {
		t.Fatal("expected error for missing remote interpreter")
	}
	if !strings.Contains(err.Error(), "prod-box") || !strings.Contains(err.Error(), "definitely-absent-xyz") {
		t.Errorf("error should name host and interpreter: %v", err)
	}
}

func TestExecRemote_JSAndGoEmbedRejected(t *testing.T) {
	e := &Executor{}
	tgt := &hosts.Target{Name: "box", Cfg: config.HostConfig{Host: "unused"}}
	for _, run := range []string{"js", "go-embed", "risor", "lua"} {
		_, err := e.Exec(context.Background(), Spec{Run: run, Code: "x", Host: tgt}, nil)
		if err == nil || !strings.Contains(err.Error(), "local-only") {
			t.Errorf("run %q: expected local-only rejection, got %v", run, err)
		}
	}
}

// ---- ParseOutputs -------------------------------------------------------------

func TestParseOutputs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]any
	}{
		{"empty", "", map[string]any{}},
		{"whitespace only", "   \n\t", map[string]any{}},
		{"json object", `{"a": 1, "b": "two"}`, map[string]any{"a": float64(1), "b": "two"}},
		{"json number", `42`, map[string]any{"value": float64(42)}},
		{"json string", `"hi"`, map[string]any{"value": "hi"}},
		{"json array", `[1,2,3]`, map[string]any{"value": []any{float64(1), float64(2), float64(3)}}},
		{"json bool", `true`, map[string]any{"value": true}},
		{"plain text", `hello world`, map[string]any{"text": "hello world"}},
		{"trims whitespace around object", "  {\"a\":1}  \n", map[string]any{"a": float64(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOutputs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseOutputs(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
