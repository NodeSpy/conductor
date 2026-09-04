package code

import (
	"context"
	"fmt"

	"github.com/risor-io/risor"
	"github.com/risor-io/risor/builtins"
	modBase64 "github.com/risor-io/risor/modules/base64"
	modBytes "github.com/risor-io/risor/modules/bytes"
	modErrors "github.com/risor-io/risor/modules/errors"
	modJSON "github.com/risor-io/risor/modules/json"
	modMath "github.com/risor-io/risor/modules/math"
	modRegexp "github.com/risor-io/risor/modules/regexp"
	modStrconv "github.com/risor-io/risor/modules/strconv"
	modStrings "github.com/risor-io/risor/modules/strings"
	modTime "github.com/risor-io/risor/modules/time"
)

// risorGlobals is the sandbox: risor's core builtins (len, keys, sprintf, …)
// plus the data-shaping modules, and NOTHING that leaves the process — no os,
// exec, http, dns, net, or filepath. risor's default global set includes all
// of those, so the executor opts out of the defaults and grants this
// allowlist explicitly (the issue's "sandboxed by the built-ins conductor
// exposes").
func risorGlobals(data map[string]any) map[string]any {
	globals := map[string]any{}
	for k, v := range builtins.Builtins() {
		globals[k] = v
	}
	globals["base64"] = modBase64.Module()
	globals["bytes"] = modBytes.Module()
	globals["errors"] = modErrors.Module()
	globals["json"] = modJSON.Module()
	globals["math"] = modMath.Module()
	globals["regexp"] = modRegexp.Module()
	globals["strconv"] = modStrconv.Module()
	globals["strings"] = modStrings.Module()
	globals["time"] = modTime.Module()
	globals["store"] = kvRisorStoreFn() // defined stores: s := store("cache"); s.get(…)
	globals["sql"] = sqlRisorFn()       // defined SQL stores: db := sql("analytics"); db.query(…)
	globals["ctx"] = data
	return globals
}

// execRisor runs a `run: risor` step: risor is a pure-Go embeddable scripting
// language (Go-flavored syntax, no cgo), executed in-process like js and
// go-embed — and, like them, local-only. The step's data is the `ctx` global;
// the script's final expression is its result (a risor map becomes the step's
// outputs, anything else lands under value:).
//
// The module identity is github.com/risor-io/risor: the repository moved to
// github.com/deepnoodle-ai/risor, but every released tag still declares the
// original module path, so that is the importable name.
func (e *Executor) execRisor(ctx context.Context, spec Spec, data map[string]any) (map[string]any, error) {
	result, err := risor.Eval(ctx, spec.Code,
		risor.WithoutDefaultGlobals(),
		risor.WithGlobals(risorGlobals(data)))
	if err != nil {
		return nil, fmt.Errorf("risor: %w", err)
	}
	if result == nil {
		return map[string]any{}, nil
	}
	return wrapValue(result.Interface()), nil
}
