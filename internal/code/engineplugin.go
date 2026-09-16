package code

import (
	"context"
	"fmt"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The PLUGIN ENGINE path: a code step whose `use:` named neither a builtin
// nor a program on the box, but an ENGINE PLUGIN — a verified, sandboxed
// subprocess the daemon already spawns and supervises (internal/plugin),
// driven here through one `plugin.run` call.
//
// It is the second shape of the SAME code-step ABI, and deliberately the
// least new thing possible:
//
//	cli / host interpreter   ctx is JSON on stdin  outputs are parsed stdout
//	PLUGIN                   ctx is RunRequest     outputs are RunResult
//
// It is also how the scripting languages arrive now: `use: js` names no
// builtin, so it resolves to conductor-plugins//engines/js and comes through
// here.
//
// and the data plane — the part that actually decides what a step may touch —
// is not a third implementation at all. `use: cli` puts a unix socket in front
// of CtxHandler (ctxsock.go); a plugin engine puts the plugin's own JSON-RPC
// transport in front of THE SAME CtxHandler, carrying THE SAME
// Spec.DataGuard. There is one authorization path in this package and both
// out-of-process engines are clients of it:
//
//	cli step   → ctxServer.answer → CtxHandler.Invoke → kv/sql/memInvoke → DataGuard
//	plugin     → Client.handleRequest → CtxHandler.InvokeHost → Invoke → … → DataGuard
//
// The two transports differ in exactly one thing — how the caller proves it is
// this run — and even there they agree: a per-run 32-byte random token minted
// by conductor, presented on every request, checked in constant time, and
// revoked the moment the step ends.

// PluginEngine is one out-of-process engine, as internal/plugin's Client
// implements it. host is called for each ctx data-plane callback the engine
// makes during the run; passing nil would run the step with NO data plane.
type PluginEngine interface {
	Run(ctx context.Context, req sdk.RunRequest, host func(sdk.HostRequest) sdk.HostResult) (map[string]any, error)
}

// EngineLookup resolves an engine name (the step's `use:` leaf) to its
// plugin. The false return is "no such engine is loaded", which is a config or
// install problem and reads as one.
type EngineLookup func(name string) (PluginEngine, bool)

// InvokeHost is CtxHandler's plugin-wire face: it converts one host.* request
// into the CtxRequest this handler already answers, and converts the answer
// back. There is NO policy here — not a check, not a default, not a special
// case — because policy is Invoke's, and a second copy of it is precisely the
// thing this whole file exists to avoid.
//
// Authentication is NOT here either, for the same reason it is not in
// ctxsock's answer() after the token check: the transport proves who is
// calling (internal/plugin's run_id registry), the handler decides what they
// may do.
func (h CtxHandler) InvokeHost(req sdk.HostRequest) sdk.HostResult {
	res := h.Invoke(CtxRequest{
		Kind:     req.Kind,
		Op:       req.Op,
		Resource: req.Resource,
		Args:     req.Args,
	})
	return sdk.HostResult{OK: res.OK, Value: res.Value, Error: res.Error, Refused: res.Refused}
}

// execPluginEngine runs a `use: <plugin-engine>` step.
func (e *Executor) execPluginEngine(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	if e.Engines == nil {
		return nil, fmt.Errorf("code: engine %q is a plugin, but no plugin engines are wired into this build", spec.Run)
	}
	eng, ok := e.Engines(spec.Run)
	if !ok {
		return nil, fmt.Errorf("code: engine %q is not loaded — it is referenced by a step but the plugin is not installed or failed to start (run `conductor init`, or check the daemon log for its load error)", spec.Run)
	}
	// The data plane, for the life of this call and no longer: the handler is
	// built per step around THIS step's guard, and internal/plugin ties the
	// token it mints to this invocation. Both halves end when Run returns.
	h := CtxHandler{Guard: spec.DataGuard}
	out, err := eng.Run(ctx, sdk.RunRequest{
		Instance: spec.Run,
		Code:     spec.Code,
		Args:     spec.Args,
		Env:      spec.Env,
		Inputs:   data,
	}, h.InvokeHost)
	if err != nil {
		return nil, fmt.Errorf("code: engine %s: %w", spec.Run, err)
	}
	if out == nil {
		// An engine that returned no outputs ran fine and produced nothing;
		// the step's outputs are empty, not nil, so a template referencing
		// one gets "missing key" rather than a nil-map panic downstream.
		out = map[string]any{}
	}
	return out, nil
}

// errRemotePluginEngine is the refusal for `use: <plugin>` + `host:`. The
// engine's subprocess belongs to this daemon and its ctx callbacks come back
// over that subprocess's own stdio, so "run it on another box" has no meaning
// that preserves either half.
func errRemotePluginEngine(name string) error {
	return fmt.Errorf("code: engine %s is a plugin of this daemon and is local-only — use a host interpreter (e.g. `use: sh`) or `use: cli` for remote code", name)
}
