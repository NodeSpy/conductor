package code

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/hosts"
)

// The `cli` engine bridges INPUTS (ctx as JSON on stdin) and OUTPUTS
// (ParseOutputs over stdout) around an argv the operator wrote.

func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
}

// command: alone — argv run as written, stdout parsed as the step's outputs.
func TestCLICommandOnly(t *testing.T) {
	needSh(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", `echo '{"ok": true, "n": 3}'`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true || toIntT(t, out["n"]) != 3 {
		t.Fatalf("outputs = %#v", out)
	}
}

// ctx arrives as JSON on the command's stdin — the same input contract the
// host-interpreter path uses, which is what makes `use: cli` a drop-in for
// `run: <interpreter>`.
func TestCLICtxOnStdin(t *testing.T) {
	needSh(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", "cat"},
	}, map[string]any{"repo": "acme/api", "pr": 7})
	if err != nil {
		t.Fatal(err)
	}
	if out["repo"] != "acme/api" || toIntT(t, out["pr"]) != 7 {
		t.Fatalf("ctx did not reach stdin: %#v", out)
	}
}

// command: + code: — the code becomes a private temp file whose path is
// APPENDED to the argv, so `command: [sh]` + code is exactly `run: sh`.
func TestCLICodeIsAppendedAsAFile(t *testing.T) {
	needSh(t)
	e := &Executor{}
	spec := Spec{Run: "cli", Command: []string{"sh"}, Code: `echo '{"from": "code"}'`}
	out, err := e.Exec(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["from"] != "code" {
		t.Fatalf("outputs = %#v", out)
	}
	// …and it is equivalent to the host-interpreter spelling.
	same, err := e.Exec(context.Background(), Spec{Run: "sh", Code: spec.Code}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if same["from"] != out["from"] {
		t.Fatalf("`use: cli, command: [sh]` and `run: sh` diverged: %#v vs %#v", out, same)
	}
}

// args: follow the code file, so a step can pass argv a script reads.
func TestCLIArgsFollowTheCodeFile(t *testing.T) {
	needSh(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh"}, Code: `printf '%s' "$1"`,
		Args: []string{"tail-arg"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["text"] != "tail-arg" {
		t.Fatalf("outputs = %#v", out)
	}
}

// A non-zero exit is an error naming the program and carrying its stderr.
func TestCLIExitFailure(t *testing.T) {
	needSh(t)
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", "echo boom >&2; exit 3"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "cli") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIProgramNotFound(t *testing.T) {
	e := &Executor{}
	_, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"conductor-no-such-program-xyz"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIEmptyCommand(t *testing.T) {
	e := &Executor{}
	if _, err := e.Exec(context.Background(), Spec{Run: "cli"}, nil); err == nil ||
		!strings.Contains(err.Error(), "no command:") {
		t.Fatalf("err = %v", err)
	}
}

// The environment is the allowlisted spawn base plus the step's own env:,
// never the daemon's — same guarantee the host-interpreter path gives.
func TestCLIEnvIsAllowlisted(t *testing.T) {
	needSh(t)
	t.Setenv("CONDUCTOR_CLI_SECRET_PROBE", "leaked")
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", `printf '%s' "${CONDUCTOR_CLI_SECRET_PROBE:-clean}/${MINE:-unset}"`},
		Env: map[string]string{"MINE": "yes"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["text"] != "clean/yes" {
		t.Fatalf("env leaked or step env: was dropped: %#v", out)
	}
}

// Remote: the same argv, framed into the generated sh script, with the code
// travelling base64-encoded and ctx on stdin.
func TestCLIRemote(t *testing.T) {
	needSh(t)
	e := &Executor{SSH: localSSH(t)}
	target := &hosts.Target{Name: "build-box"}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh"}, Code: `cat`, Host: target,
	}, map[string]any{"who": "remote"})
	if err != nil {
		t.Fatal(err)
	}
	if out["who"] != "remote" {
		t.Fatalf("outputs = %#v", out)
	}
}

func TestCLIRemoteNotFound(t *testing.T) {
	needSh(t)
	e := &Executor{SSH: localSSH(t)}
	_, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"conductor-no-such-program-xyz"},
		Host: &hosts.Target{Name: "build-box"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found on host build-box") {
		t.Fatalf("err = %v", err)
	}
}

// An argv word containing quotes, newlines and a semicolon reaches the
// remote program intact rather than being reinterpreted by the generated
// script's own shell.
func TestCLIRemoteArgvQuoting(t *testing.T) {
	needSh(t)
	e := &Executor{SSH: localSSH(t)}
	nasty := `a'b"c; echo pwned` + "\n" + `d`
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", `printf '%s' "$1"`, "sh", nasty},
		Host: &hosts.Target{Name: "build-box"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["text"] != nasty {
		t.Fatalf("argv word mangled: %q", out["text"])
	}
}

func TestRemoteCLIScriptShape(t *testing.T) {
	s := remoteCLIScript([]string{"make", "test"}, "", "build-box", nil)
	if strings.Contains(s, "base64 -d") {
		t.Errorf("no code: means no code frame:\n%s", s)
	}
	if !strings.Contains(s, "command -v 'make'") || !strings.HasSuffix(s, "exec 'make' 'test'") {
		t.Errorf("script = %s", s)
	}
	s = remoteCLIScript([]string{"python3"}, "print(1)", "build-box", []string{"--flag"})
	if !strings.Contains(s, "base64 -d > \"$t/code\"") {
		t.Errorf("code must travel base64-framed:\n%s", s)
	}
	if !strings.HasSuffix(s, `exec 'python3' "$t/code" '--flag'`) {
		t.Errorf("script = %s", s)
	}
}
