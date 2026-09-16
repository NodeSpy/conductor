package main

import (
	"os"

	"github.com/NodeSpy/conductor/internal/code"
)

// runCtx is the `conductor ctx` entry point: the reference client for a
// `use: cli` step's ctx data plane (internal/code/ctxclient.go). It is only
// meaningful INSIDE such a step, where the engine exported
// CONDUCTOR_CTX_SOCK/CONDUCTOR_CTX_TOKEN into the command's environment and
// CONDUCTOR_CTX_HELPER points at this binary:
//
//	run: { id: bump, use: cli, command: [bash], code: |
//	  n=$("$CONDUCTOR_CTX_HELPER" ctx kv cache incr run attempts 1)
//	  printf '{"attempts": %s}' "$n" }
//
// Run anywhere else it says so and exits 1 — it holds no credential of its
// own, and every op it forwards is authorized host-side by the daemon that
// minted the socket.
func runCtx(args []string) int {
	return code.CtxClientMain(args, os.Getenv, os.Stdout, os.Stderr)
}
