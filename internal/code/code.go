// Package code executes `run:`/`use:`-form code steps: short snippets of
// script attached directly to a trigger/workflow instead of a full `type:
// agent` or `uses: <connector>.<verb>` step.
//
// NOTHING interprets a language in this process any more. There is exactly
// ONE builtin engine, `cli` (cli.go): it runs the step's own `command:` argv
// as a subprocess — local or remote — and is the general form of the host
// interpreter. Everything else is out of this binary:
//
//	cli                    a subprocess of this daemon, local or over host:
//	sh/bash/node/python/…   a real interpreter on PATH (hostinterp.go), or a
//	                        path to one; `go` compiles through the host
//	                        toolchain (gorun.go)
//	js/lua/risor/go-embed/… an ENGINE PLUGIN — a verified subprocess fetched
//	                        from conductor-plugins//engines/<name> and driven
//	                        over the plugin wire (engineplugin.go)
//
// The four scripting engines used to be linked in (QuickJS, gopher-lua,
// Risor, yaegi). They are now official engine plugins, which is why a config
// that says `use: js` still works: the name resolves as a non-builtin engine
// and dispatches through plugin.run instead of an in-process interpreter.
// The interpreters, their sandboxes, and their ~20 MB of dependencies left
// the binary with them.
//
// Every engine shares the same calling convention: the step's template
// context reaches the code as `ctx` (a JSON document on stdin for cli and
// host interpreters, the RunRequest inputs for a plugin), and the code's
// result becomes the step's outputs — ParseOutputs over stdout, or the
// plugin's RunResult — so a trigger's `if:`/templates can reference
// `{{.steps.<id>.outputs.foo}}` the same way regardless of which engine
// produced it.
package code

import (
	"context"
	"log"
	"os/exec"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Spec is one code step, already resolved by the caller: `host:`/inline
// `ssh:` have been turned into a *hosts.Target (or left nil for local), and
// Code is the literal script/program text (or, for a host interpreter whose
// Run field is a path, the interpreter path is Run itself — see Exec).
type Spec struct {
	// Run selects the engine: "cli" | "go" | a host interpreter name (sh,
	// bash, ruby, node, python, perl, php, …) | an absolute or relative path
	// to one (anything containing '/') | the name of an ENGINE PLUGIN (js,
	// lua, …), in which case Plugin is set. It carries the step's `use:`
	// when the step wrote that spelling — the two are one selection
	// (internal/config engines.go).
	Run string
	// Command is the argv the "cli" engine runs. Ignored by every other
	// engine, which take their work as Code. See cli.go for how the two
	// compose when a cli step sets both.
	Command []string
	// Plugin marks Run as naming an out-of-process PLUGIN engine rather than
	// a builtin or a host interpreter. It is a classification the CONFIG
	// layer already made (config.EnginePlugin — a `use:` that is neither a
	// builtin nor a path nor a known interpreter name), carried here so this
	// package does not have to re-derive it from the string and reach a
	// different answer than the validator did.
	Plugin bool
	// Code is the script/program source.
	Code string
	// Args are extra argv entries after the code file, for host
	// interpreters and the cli engine; an engine plugin receives them on the
	// RunRequest and decides for itself what they mean.
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
	// set `host:`/inline `ssh:`. Only host interpreters and `cli` may run
	// remotely — a plugin engine is a subprocess of THIS daemon and is
	// local-only (see Exec).
	Host *hosts.Target
	// DataGuard, when non-nil, is consulted before every DURABLE WRITE the
	// ctx data plane performs (ctx.store set/setnx/merge/append,
	// ctx.sql exec, ctx.memory remember) — the code-sandbox face of the plan
	// write barrier: without it, an agent plan's code step could park secret
	// material that `uses: kv.set` would have refused.
	DataGuard DataGuard
	// Isolation is the step's OWN `isolation:` block (config.Step.Isolation),
	// carried through unresolved — nil unless the step explicitly configured
	// one. A nil Isolation must leave a code step running exactly as it
	// always has: BARE, no sandbox.WrapLocalCommand call at all. Only
	// execCLILocal/execHostLocal honor it today (LOCAL cli/host-interpreter
	// steps); a remote (Host != nil) or plugin-engine step never reaches a
	// wrap here — see internal/config/isolation.go's validateStepIsolation
	// for what that refuses at load time rather than silently ignoring.
	Isolation *config.IsolationConfig
	// IsolationDefaulted marks Isolation as one conductor SYNTHESIZED for a
	// pack's code step (confined-by-default), not one the author wrote. When
	// true, a sandbox that cannot be realized here degrades to a bare run with
	// a warning (best-effort); when false (an explicit isolation:), the same
	// failure fails the step closed.
	IsolationDefaulted bool
}

// DataGuard vets one binding write: kind is kv|sql|memory, op the operation,
// args the caller-supplied values about to be written. A non-nil error
// refuses the write.
// DataGuard vets a code step's data-plane calls: kind is kv|sql|memory, op
// the operation, resource the store name (kv/sql; "" for memory), args the
// caller-supplied arguments. Called on EVERY kv/sql/memory op — the guard
// implementation decides which ops it cares about (the plan write barrier
// vets value writes; the #124 resource allowlist vets any touch of a store).
type DataGuard func(kind, op, resource string, args []any) error

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
	// Engines resolves a PLUGIN engine name to the running plugin that
	// implements it (internal/plugin's Manager, wired in cmd). nil means this
	// build has no plugin engines, and a `use: <plugin>` step says so rather
	// than falling through to a PATH lookup for a program nobody named.
	Engines EngineLookup
	// Sandbox is the daemon-side sandbox wiring a code step's `isolation:`
	// block wraps through — sandbox.WrapLocalCommand's deps (egress proxy
	// minters, daemon mask paths, self-exe resolver), the same wiring
	// controller.prepareLaunch and plugin.SandboxDeps reuse for their own
	// local launches (see cmd/conductor). The zero value is a LEGAL, if
	// limited, executor: a spec with no Isolation runs exactly as before
	// regardless, and a spec WITH Isolation but no Sandbox wiring fails
	// closed through WrapLocalCommand's own nil-dep checks rather than
	// running unconfined.
	Sandbox sandbox.LocalWrapDeps
	// Warn logs a non-fatal warning (the best-effort sandbox degrade: a pack's
	// default confinement that cannot be realized on this box runs bare +
	// warns). nil falls back to the standard logger.
	Warn func(format string, args ...any)
}

// warnf emits a non-fatal warning through the wired hook, or the standard
// logger when none is set.
func (e *Executor) warnf(format string, args ...any) {
	if e.Warn != nil {
		e.Warn(format, args...)
		return
	}
	log.Printf(format, args...)
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
// step's outputs (see ParseOutputs for the exact contract). Dispatch is: a
// PLUGIN engine goes out over the plugin wire (local-only); a remote spec
// (Host != nil) goes over SSH through the cli or host-interpreter path; a
// local spec dispatches on Run to `cli` or the Go toolchain, falling through
// to the local host-interpreter path for anything else.
func (e *Executor) Exec(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	// A plugin engine is checked BEFORE the host/remote split: it runs in a
	// subprocess of THIS daemon holding a JSON-RPC transport back to it, so
	// there is nothing to ship over ssh — its ctx callbacks would have to come
	// back across the hop.
	if spec.Plugin {
		if spec.Host != nil {
			return nil, errRemotePluginEngine(spec.Run)
		}
		return e.execPluginEngine(ctx, spec, data)
	}
	// `code: file:<path>` loads the source from a file on the daemon (host
	// interpreters, cli, go). Resolved here, after the plugin check, so a
	// plugin engine still receives its code: verbatim over the wire.
	resolved, ferr := resolveCodeFile(spec.Code)
	if ferr != nil {
		return nil, ferr
	}
	spec.Code = resolved
	if spec.Host != nil {
		return e.execRemote(ctx, spec, data)
	}
	switch spec.Run {
	case "cli":
		return e.execCLILocal(ctx, spec, data)
	case "go":
		return e.execGoToolchain(ctx, spec, data)
	default:
		return e.execHostLocal(ctx, spec, data)
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
