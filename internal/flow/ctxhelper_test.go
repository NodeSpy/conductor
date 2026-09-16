package flow

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/code"
)

// Test harness for CODE STEPS THAT TOUCH THE DATA PLANE.
//
// These flow tests used to write `run: js` and call ctx.store(…)/ctx.sql(…)
// directly, because the js engine held a Go binding into this process. The
// scripting engines are plugins now, so the in-tree engine that can still
// reach kv/sql/memory is `cli` — and it reaches them the way every
// out-of-process engine does: over the per-run socket, asking conductor to
// perform each op (internal/code ctxsock.go + CtxHandler).
//
// That needs a client on the other end. A real deployment gets conductor's
// own binary as $CONDUCTOR_CTX_HELPER; a test binary is not conductor, so
// the same trick internal/code's ctxcli_test.go uses applies here: re-exec
// THIS test binary in a mode where it runs the shipped CtxClientMain. The
// step then speaks the real protocol to the real handler under the real
// DataGuard — which is what these tests are actually about.

// flowCtxHelperEnv switches the test binary into "be the reference client"
// mode (see TestFlowCtxClientHelperProcess).
const flowCtxHelperEnv = "CONDUCTOR_FLOW_CTX_TEST_CLIENT"

// TestFlowCtxClientHelperProcess is not a test: re-executed with
// CONDUCTOR_FLOW_CTX_TEST_CLIENT=1 it IS the `conductor ctx` client, so the
// code steps below exercise the shipped client rather than a second
// implementation written for the occasion.
func TestFlowCtxClientHelperProcess(t *testing.T) {
	if os.Getenv(flowCtxHelperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by a code-step test")
	}
	args := []string{}
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	// The real command line is `conductor ctx <kind> …`; main.go strips the
	// subcommand word before calling CtxClientMain, so strip it here too and
	// the snippets read exactly as an operator would write them.
	if len(args) > 0 && args[0] == "ctx" {
		args = args[1:]
	}
	os.Exit(code.CtxClientMain(args, os.Getenv, os.Stdout, os.Stderr))
}

// ctxHelper writes a wrapper standing in for the conductor binary and
// returns its path, for interpolation into a step's `env:`. Skips the test
// when there is no sh to run the code steps with.
func ctxHelper(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "conductor")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestFlowCtxClientHelperProcess -- \"$@\"\n", self)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
