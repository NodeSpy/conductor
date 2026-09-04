// Package code executes `run:`-form code steps: short snippets of script
// attached directly to a trigger/workflow instead of a full `type: agent` or
// `uses: <connector>.<verb>` step. Two engines run baked into the conductor
// binary (`js` via an embedded QuickJS, `go-embed` via the yaegi Go
// interpreter) — both in-process, sandboxed, and therefore LOCAL ONLY: they
// share this process's fate (crash it, hang it, exhaust its memory) so they
// must never be handed to a remote box conductor doesn't control the
// lifecycle of. Everything else (`sh`, `bash`, `node`, `python`, a bare
// `go`, or an absolute/relative interpreter path) shells out to a real
// interpreter on PATH — locally, or on a named `hosts:`/inline `ssh:` target
// over internal/hosts when the step sets `host:`.
//
// Every engine shares the same calling convention: the step's template
// context is exposed to the code as `ctx` (a JS global, a Go map argument, a
// JSON document on stdin — whichever fits the engine), and the code's
// result becomes the step's outputs via the shared ParseOutputs contract, so
// a trigger's `if:`/templates can reference `{{.steps.<id>.outputs.foo}}`
// the same way regardless of which `run:` engine produced it.
package code

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/NodeSpy/paseo-conductor/internal/hosts"
)

// Spec is one code step, already resolved by the caller: `host:`/inline
// `ssh:` have been turned into a *hosts.Target (or left nil for local), and
// Code is the literal script/program text (or, for a host interpreter whose
// Run field is a path, the interpreter path is Run itself — see Exec).
type Spec struct {
	// Run selects the engine: "js" | "go-embed" | "go" | a host interpreter
	// name (sh, bash, ruby, node, python, perl, php, …) | an absolute or
	// relative path to one (anything containing '/').
	Run string
	// Code is the script/program source.
	Code string
	// Args are extra argv entries after the code file, for host
	// interpreters (ignored by js/go-embed, which have no argv).
	Args []string
	// Env are extra environment variables for host interpreters. Locally
	// they're appended after os.Environ(); remotely they're `export`ed
	// inside the remote shell (see internal/hosts.Client.Script) — either
	// way, never placed on argv.
	Env map[string]string
	// WorkDir is the working directory for host interpreters (local: cmd
	// dir; remote: falls back to the target's configured Cwd).
	WorkDir string
	// Host is nil for a local step, or the resolved target for a step that
	// set `host:`/inline `ssh:`. Only host interpreters may run remotely —
	// js/go-embed are local-only (see Exec).
	Host *hosts.Target
}

// Executor runs code steps. The zero value is usable: SSH defaults to a
// zero-value *hosts.Client (real ssh subprocess), LookPath defaults to
// exec.LookPath. Both are exported so tests can inject fakes without
// touching the real filesystem/network.
type Executor struct {
	// SSH runs remote (host-interpreter) specs. nil means a fresh, default
	// *hosts.Client per call.
	SSH *hosts.Client
	// LookPath resolves an interpreter name to an executable path (default
	// exec.LookPath). Overridable so tests can simulate "not installed"
	// without mutating PATH.
	LookPath func(string) (string, error)
}

func (e *Executor) lookPath() func(string) (string, error) {
	if e.LookPath != nil {
		return e.LookPath
	}
	return exec.LookPath
}

func (e *Executor) sshClient() *hosts.Client {
	if e.SSH != nil {
		return e.SSH
	}
	return &hosts.Client{}
}

// Exec runs spec, exposing data to the code as `ctx` and returning the
// step's outputs (see ParseOutputs / the in-process wrapValue for the exact
// per-engine contract). Dispatch is: a remote spec (Host != nil) always goes
// through the host-interpreter path over SSH (js/go-embed reject remote —
// see execRemote); a local spec dispatches on Run to the matching in-process
// engine, falling through to the local host-interpreter path for anything
// else.
func (e *Executor) Exec(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	if spec.Host != nil {
		return e.execRemote(ctx, spec, data)
	}
	switch spec.Run {
	case "js":
		return e.execJS(spec, data)
	case "go-embed":
		return e.execGoEmbed(spec, data)
	case "risor":
		return e.execRisor(ctx, spec, data)
	case "lua":
		return e.execLua(spec, data)
	case "go":
		return e.execGoToolchain(ctx, spec, data)
	default:
		return e.execHostLocal(ctx, spec, data)
	}
}

// wrapValue applies the in-process (js/go-embed) half of the output
// contract to a decoded/returned Go value: an object (map) becomes the
// outputs map as-is (its keys are the step's named outputs); nil (JS
// null/undefined, or a bare `return` in either engine) becomes no outputs;
// anything else (a string, number, bool, slice, …) is not a map of named
// outputs, so it becomes the step's single `value` output instead of being
// silently dropped or erroring.
func wrapValue(v any) map[string]any {
	switch t := v.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return t
	default:
		return map[string]any{"value": v}
	}
}

// shQuote single-quotes s for POSIX sh (see internal/hosts.shQuote, which
// this mirrors byte-for-byte): the remote-script builder in hostinterp.go
// needs its own copy since it isn't part of the hosts package, but the
// escaping rule itself (wrap in single quotes, close/escape/reopen around
// each embedded single quote) is the one safe way to do this regardless of
// which package is doing it.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hostLabel names a target in error messages: its configured name, falling
// back to the bare address for inline `ssh:` targets that have no name.
func hostLabel(t *hosts.Target) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Cfg.Host
}

// errRemoteInProcessEngine is returned by execRemote for js/go-embed: those
// engines run the code inside conductor's own WASM/interpreter sandbox in
// *this* process, so there is nothing meaningful to ship to a remote host —
// the config validator (internal/config) already rejects this combination
// structurally, but Exec guards it too since callers can construct a Spec
// directly (e.g. from a workflow `use:` expansion) without going through
// config validation.
func errRemoteInProcessEngine(run string) error {
	return fmt.Errorf("code: run: %s executes inside conductor's own process and is local-only — use a host interpreter (e.g. run: node/run: sh) for remote code", run)
}
