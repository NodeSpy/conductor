package code

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hosts"
)

// TestExecGoToolchainErrors: a missing toolchain says the box needs one; a
// compile failure carries the compiler's stderr.
func TestExecGoToolchainErrors(t *testing.T) {
	missing := &Executor{LookPath: func(string) (string, error) { return "", fmt.Errorf("nope") }}
	_, err := missing.Exec(context.Background(), Spec{Run: "go", Code: "package main"}, nil)
	if err == nil || !strings.Contains(err.Error(), "go not found on PATH") {
		t.Fatalf("missing toolchain must say so: %v", err)
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
