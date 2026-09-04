package code

import (
	"encoding/json"
	"fmt"

	"github.com/fastschema/qjs"
)

// execJS runs `run: js` under QuickJS (via qjs, a pure-Go/WASM build —
// wazero under the hood, no cgo, no external quickjs install) entirely
// in-process. This is the "quick, sandboxed, no toolchain needed" engine:
// good for small string/JSON transforms in a trigger's step chain where
// spinning up a real interpreter (or the Go toolchain) would be overkill,
// and safe by construction — QuickJS-in-WASM has no filesystem or network
// access into this process's world unless conductor explicitly wires one
// up, which it doesn't here.
//
// data becomes `ctx` as a plain JS value (JSON round-tripped in, so only
// JSON-shaped data survives — no functions, no cycles): the source handed
// to QuickJS is built as
//
//	globalThis.ctx = <JSON of data>;
//	JSON.stringify((function(){
//	<user code>
//	})() ?? null)
//
// i.e. the user's code becomes the *body* of an IIFE whose return value is
// what the step produces (a bare `return 5` from top-level code would be a
// syntax error, which is why it has to be wrapped in a function first); the
// `?? null` turns "no return statement" (JS undefined) into JSON-serializable
// null rather than JSON.stringify silently producing the string
// "undefined". The stringified result is then JSON-decoded back into a Go
// `any` and handed to wrapValue for the object/nil/scalar contract.
func (e *Executor) execJS(spec Spec, data map[string]any) (map[string]any, error) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("code: js: marshal ctx: %w", err)
	}

	if string(dataJSON) == "null" { // no data → an empty ctx, so ctx.kv still attaches
		dataJSON = []byte("{}")
	}
	src := "globalThis.ctx = " + string(dataJSON) + ";\n" +
		jsKVShim() +
		"JSON.stringify((function(){\n" + spec.Code + "\n})() ?? null)"

	rt, err := qjs.New()
	if err != nil {
		return nil, fmt.Errorf("code: js: create runtime: %w", err)
	}
	defer rt.Close()

	// ctx.store bridges to the defined stores through one host function
	// taking and returning JSON (see kvInvokeJSON) — values cross the WASM
	// boundary as strings, so no Value plumbing per type.
	qctx := rt.Context()
	hostKV := qctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		payload := ""
		if args := this.Args(); len(args) > 0 {
			payload = args[0].String()
		}
		return this.Context().NewString(kvInvokeJSON(payload)), nil
	})
	qctx.Global().SetPropertyStr("__conductor_kv", hostKV)

	ret, err := qctx.Eval("step.js", qjs.Code(src))
	if err != nil {
		return nil, fmt.Errorf("code: js: %w", err)
	}
	defer ret.Free()

	resultJSON := ret.String()
	var v any
	if err := json.Unmarshal([]byte(resultJSON), &v); err != nil {
		return nil, fmt.Errorf("code: js: decode result %q: %w", resultJSON, err)
	}
	return wrapValue(v), nil
}

// jsKVShim builds ctx.store over the __conductor_kv host bridge:
// ctx.store("cache") returns an object whose ops serialize their args to
// JSON; a bridge error becomes a thrown Error. Absent reads come back null.
func jsKVShim() string {
	ops, _ := json.Marshal(kvOps)
	return `ctx.store = (store) => {
  const call = (op, args) => {
    const r = JSON.parse(__conductor_kv(JSON.stringify({ store, op, args })));
    if (r.err) throw new Error(r.err);
    return r.v ?? null;
  };
  const o = {};
  for (const op of ` + string(ops) + `) o[op] = (...args) => call(op, args);
  return o;
};
`
}
