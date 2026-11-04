package code

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// isolation: is OPT-IN and per-step: a Spec with no Isolation must run BARE,
// exactly as it always has — no sandbox.WrapLocalCommand call at all, no
// change in behavior. TestCLICommandOnly and friends already exercise that
// path with Isolation left at its zero value (nil); this file is about the
// step that DOES set one.
//
// Pinning sandbox.CheckGOOS to a non-Linux value makes namespace mode's
// Check() refuse deterministically, with no real sandbox binaries (unshare,
// sudo, docker) or actual privilege boundary involved — the cleanest way to
// prove the isolation: block on a `Spec` is actually reaching
// sandbox.WrapLocalCommand rather than being read and silently dropped, the
// bug this file guards against.
func stubNonLinux(t *testing.T) {
	t.Helper()
	old := sandbox.CheckGOOS
	sandbox.CheckGOOS = "windows"
	t.Cleanup(func() { sandbox.CheckGOOS = old })
}

// A `use: cli` Spec with Isolation set routes execCLILocal through
// sandbox.WrapLocalCommand: on a platform namespace mode refuses, the step
// fails closed with an error naming both the isolation wrap and the
// underlying platform refusal, instead of silently running the bare command
// (which would have printed the `{"ok": true}` JSON and succeeded).
func TestCLIIsolationRoutesThroughWrap(t *testing.T) {
	needSh(t)
	stubNonLinux(t)

	// A code step's namespace isolation is realized as the pivot_root fs-jail,
	// which resolves conductor's own binary before the platform Check; a stub
	// self-exe lets the wrap proceed to that Check, which refuses off-Linux.
	e := &Executor{Sandbox: sandbox.LocalWrapDeps{SelfExe: func() (string, error) { return "/conductor", nil }}}
	spec := Spec{
		Run:       "cli",
		Command:   []string{"sh", "-c", `echo '{"ok": true}'`},
		Isolation: &config.IsolationConfig{Mode: "namespace"},
	}
	_, err := e.Exec(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("a code step with isolation: must fail closed when the sandbox can't be realized, not fall back to running bare")
	}
	if !strings.Contains(err.Error(), "code: cli: isolation:") || !strings.Contains(err.Error(), "Linux user namespaces or macOS Seatbelt") {
		t.Fatalf("error must show the isolation wrap was engaged: %v", err)
	}
}

// The same routing for a host-interpreter step (`run: sh` / `run: bash`,
// execHostLocal) — the other local code-execution path isolation: must wrap.
func TestHostInterpIsolationRoutesThroughWrap(t *testing.T) {
	needSh(t)
	stubNonLinux(t)

	e := &Executor{Sandbox: sandbox.LocalWrapDeps{SelfExe: func() (string, error) { return "/conductor", nil }}}
	spec := Spec{
		Run:       "sh",
		Code:      `echo '{"ok": true}'`,
		Isolation: &config.IsolationConfig{Mode: "namespace"},
	}
	_, err := e.Exec(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("a code step with isolation: must fail closed when the sandbox can't be realized, not fall back to running bare")
	}
	if !strings.Contains(err.Error(), "code: sh: isolation:") || !strings.Contains(err.Error(), "Linux user namespaces or macOS Seatbelt") {
		t.Fatalf("error must show the isolation wrap was engaged: %v", err)
	}
}

// `run: go` (execGoToolchain) must route through sandbox.WrapLocalCommand
// exactly like cli/host-interpreter steps when it sets isolation: — this is
// rt2 #4: execGoToolchain used to ignore spec.Isolation entirely and always
// run the go toolchain bare, even for a pack's confined-by-default `run: go`
// step. Reverting the gorun.go wrap makes this fail (no error at all — the
// bare `go run` would just execute the stub program) rather than fail closed
// here.
func TestGoIsolationRoutesThroughWrap(t *testing.T) {
	stubNonLinux(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}

	e := &Executor{Sandbox: sandbox.LocalWrapDeps{SelfExe: func() (string, error) { return "/conductor", nil }}}
	spec := Spec{
		Run:       "go",
		Code:      "package main\nfunc main() {}\n",
		Isolation: &config.IsolationConfig{Mode: "namespace"},
	}
	_, err := e.Exec(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("a run: go step with isolation: must fail closed when the sandbox can't be realized, not fall back to running bare")
	}
	if !strings.Contains(err.Error(), "code: go: isolation:") || !strings.Contains(err.Error(), "Linux user namespaces or macOS Seatbelt") {
		t.Fatalf("error must show the isolation wrap was engaged: %v", err)
	}
}

// A pack's confined-by-default `run: go` step (IsolationDefaulted) degrades
// to a bare run + warning when the sandbox can't be realized — best-effort,
// like the cli/host-interpreter paths — rather than failing the step.
func TestGoIsolationDefaultedDegradesToWarning(t *testing.T) {
	stubNonLinux(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}

	var warned string
	e := &Executor{
		Sandbox: sandbox.LocalWrapDeps{SelfExe: func() (string, error) { return "/conductor", nil }},
		Warn:    func(format string, args ...any) { warned = fmt.Sprintf(format, args...) },
	}
	spec := Spec{
		Run:                "go",
		Code:               "package main\nfunc main() {}\n",
		Isolation:          &config.IsolationConfig{Mode: "namespace"},
		IsolationDefaulted: true,
	}
	if _, err := e.Exec(context.Background(), spec, nil); err != nil {
		t.Fatalf("a defaulted isolation must degrade to a bare run, not fail: %v", err)
	}
	if warned == "" || !strings.Contains(warned, "default sandbox unavailable") {
		t.Fatalf("expected a best-effort warning, got %q", warned)
	}
}

// No Isolation set (the zero value) must never touch sandbox.WrapLocalCommand
// at all — pinning CheckGOOS to a value that would refuse EVERY isolation
// mode proves a bare step doesn't even ask the question.
func TestGoNoIsolationNeverConsultsSandbox(t *testing.T) {
	stubNonLinux(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}

	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "go",
		Code: `package main

import "fmt"

func main() { fmt.Print(` + "`" + `{"ok": true}` + "`" + `) }
`,
	}, nil)
	if err != nil {
		t.Fatalf("a step with no isolation: must run bare regardless of sandbox.CheckGOOS: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("outputs = %#v", out)
	}
}

// No Isolation set (the zero value) must never touch sandbox.WrapLocalCommand
// at all — pinning CheckGOOS to a value that would refuse EVERY isolation
// mode proves a bare step doesn't even ask the question.
func TestCLINoIsolationNeverConsultsSandbox(t *testing.T) {
	needSh(t)
	stubNonLinux(t)

	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{
		Run: "cli", Command: []string{"sh", "-c", `echo '{"ok": true}'`},
	}, nil)
	if err != nil {
		t.Fatalf("a step with no isolation: must run bare regardless of sandbox.CheckGOOS: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("outputs = %#v", out)
	}
}
