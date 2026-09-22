package code

import (
	"context"
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
