// Command acme-engine is a REFERENCE external STEP-ENGINE plugin for
// conductor, built against the PUBLIC plugin SDK
// (github.com/NodeSpy/conductor/pkg/plugin) — it imports NO internal daemon
// package, exactly as a third-party engine in its own module would.
//
// It is the engine-side twin of acme-echo (the reference CONNECTOR): where
// that one proves the daemon→plugin verb path, this one proves the two things
// a step engine adds.
//
//  1. plugin.run — conductor hands it ONE code step (inputs, code, args, env)
//     and it hands back the step's outputs, out of process.
//  2. host.* — DURING that run it calls BACK into conductor for ctx data-plane
//     ops. It holds no store and no credential; it asks, and conductor decides
//     each time against the step's own guard. The denial path is exercised on
//     purpose: DENY_STORE/DENY_SQL name stores the engine is meant NOT to
//     reach, and a run that is allowed there is a failing run.
//
// The step's contract, such as it is. The KNOBS arrive in `env:` — a step's
// own configuration, delivered per-call over the transport, exactly as a `use:
// cli` step's env: is — while `inputs` is the rendered ctx document (the
// trigger's facts), which the engine reports back rather than reads:
//
//	env.STORE / env.NS / env.KEY / env.VALUE   where to write, and what (host.kv)
//	env.SQL_STORE / env.SQL_TABLE              the same round trip over host.sql
//	env.DENY_STORE / env.DENY_SQL              stores conductor must NOT allow
//	env.ECHO                                   an inputs key to hand back
//	code:                                      carried through, reported by length
//
//	outputs.engine      "acme-engine"
//	outputs.roundtrip   the value as READ BACK through conductor
//	outputs.denied      true when the deny target was not allowed, at all
//	outputs.refused     true when that denial was a POLICY refusal specifically
//	outputs.denial      the message, whichever kind it was
//	outputs.code_len    len(code), proving the body crossed the wire intact
//	outputs.args        the step's args, echoed
//	outputs.inputs_seen how many ctx keys arrived
//	outputs.echo        inputs[env.ECHO], proving ctx crossed with its values
//
// Both faces are here because both kinds of denial are worth showing: a
// DataGuard refusal (refused:true — the step's own policy) and a store-level
// gate such as `code_access: none` or an undefined store (refused:false, but
// denied all the same). An engine that conflated them would give an operator
// the wrong thing to go fix.
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"context"
	"fmt"
	"os"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

func describe() plugin.Decl {
	return plugin.Decl{
		// Kind AND ABI are what make this an engine on the wire. A plugin that
		// set neither would be accepted as the connector it looks like — which
		// is exactly why the daemon cross-checks both against the block the
		// reference appeared in.
		Kind: plugin.KindStep,
		ABI:  plugin.EngineABI,
		Type: "acme-engine",
		Desc: "reference step engine (example plugin): echoes inputs and round-trips one kv op through the host",
		// No egress, no fs, no spawn. An engine that declares no network gets
		// deny-by-default at spawn, which is the right answer for one that
		// only ever talks to conductor.
		Capabilities: plugin.Capabilities{},
	}
}

func run(ctx context.Context, req plugin.RunRequest, host *plugin.Host) (plugin.RunResult, error) {
	fmt.Fprintf(os.Stderr, "acme-engine: run instance=%s inputs=%d data-plane=%v\n",
		req.Instance, len(req.Inputs), host.Available())

	str := func(k string) string { return req.Env[k] }
	out := map[string]any{
		"engine":      "acme-engine",
		"code_len":    len(req.Code),
		"args":        req.Args,
		"inputs_seen": len(req.Inputs),
	}
	if k := str("ECHO"); k != "" {
		out["echo"] = req.Inputs[k]
	}

	// The host round trip: write, then read back through conductor. The value
	// never passes through a store this process holds — there is no such
	// store here, which is the whole point.
	store, ns, key, value := str("STORE"), str("NS"), str("KEY"), str("VALUE")
	if store != "" && host.Available() {
		if err := host.KV().Set(ctx, store, ns, key, value); err != nil {
			return plugin.RunResult{}, fmt.Errorf("kv set: %w", err)
		}
		got, err := host.KV().Get(ctx, store, ns, key)
		if err != nil {
			return plugin.RunResult{}, fmt.Errorf("kv get: %w", err)
		}
		out["roundtrip"] = got
	}

	// The same round trip over the sql face, for a deployment whose stores are
	// relational. One op set, one authorization path, two spellings.
	if sqlStore, table := str("SQL_STORE"), str("SQL_TABLE"); sqlStore != "" && host.Available() {
		if _, err := host.SQL().Exec(ctx, sqlStore,
			"CREATE TABLE IF NOT EXISTS "+table+" (k TEXT PRIMARY KEY, v TEXT)"); err != nil {
			return plugin.RunResult{}, fmt.Errorf("sql create: %w", err)
		}
		if _, err := host.SQL().Exec(ctx, sqlStore,
			"INSERT OR REPLACE INTO "+table+" (k, v) VALUES (?, ?)", key, value); err != nil {
			return plugin.RunResult{}, fmt.Errorf("sql insert: %w", err)
		}
		rows, err := host.SQL().Query(ctx, sqlStore, "SELECT v FROM "+table+" WHERE k = ?", key)
		if err != nil {
			return plugin.RunResult{}, fmt.Errorf("sql select: %w", err)
		}
		out["roundtrip"] = firstColumn(rows)
	}

	// The DENIAL path. A denial here is the CORRECT outcome, so it is reported
	// rather than returned as an error — and an engine that got a value back
	// reports denied:false, which is what makes the assertion meaningful in
	// both directions.
	if deny := str("DENY_STORE"); deny != "" && host.Available() {
		_, err := host.KV().Get(ctx, deny, ns, key)
		report(out, err)
	}
	if deny := str("DENY_SQL"); deny != "" && host.Available() {
		_, err := host.SQL().Query(ctx, deny, "SELECT 1")
		report(out, err)
	}

	// A no-data-plane mode, for the remote/unauthorized case: the engine says
	// so rather than failing the step.
	if !host.Available() {
		out["no_data_plane"] = true
	}
	return plugin.RunResult{Outputs: out}, nil
}

// report records how a deliberately-denied call came back. It keeps the two
// denials distinct: `refused` is conductor's POLICY saying no (the step's
// DataGuard), `denied` is any not-allowed outcome including a store-level gate
// such as `code_access: none`. An engine that reported only one of them would
// send the operator to the wrong file.
func report(out map[string]any, err error) {
	switch {
	case plugin.IsRefused(err):
		out["denied"], out["refused"], out["denial"] = true, true, err.Error()
	case err != nil:
		out["denied"], out["refused"], out["denial"] = true, false, err.Error()
	default:
		out["denied"], out["refused"] = false, false
	}
}

// firstColumn pulls the single value out of a one-row, one-column query
// result. The rows come back as whatever JSON conductor sent, so this is
// defensive by necessity rather than by taste.
func firstColumn(rows any) any {
	list, ok := rows.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	row, ok := list[0].(map[string]any)
	if !ok {
		return list[0]
	}
	for _, v := range row {
		return v
	}
	return nil
}

func main() {
	if err := plugin.Serve(plugin.EngineFunc(describe, run)); err != nil {
		fmt.Fprintf(os.Stderr, "acme-engine: serve: %v\n", err)
		os.Exit(1)
	}
}
