package code

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// goEmbedAllowlist is the set of stdlib import paths a `run: go-embed`
// snippet may `import`. This is conductor's actual sandbox boundary for the
// engine (yaegi itself has no notion of "forbidden package" — it can only
// resolve packages it's been given source or Use()-registered binary
// symbols for): anything not in this list, including "os", "os/exec",
// "net", "net/http", "io", and the interpreter's own "reflect"/"unsafe"
// escape hatches, is simply never registered, so an `import` of it fails
// with yaegi's ordinary "unable to find source related to" error — there is
// no other code path to smuggle process/filesystem/network access through.
// The list itself is deliberately narrow: general-purpose data-shaping
// packages (string/byte/JSON handling, time, math, sorting, regex, basic
// URL parsing) that a template-adjacent code step plausibly needs, and
// nothing that touches the outside world.
var goEmbedAllowlist = map[string]bool{
	"bytes":           true,
	"encoding/json":   true,
	"encoding/base64": true,
	"errors":          true,
	"fmt":             true,
	"math":            true,
	"math/rand":       true,
	"net/url":         true,
	"path":            true,
	"regexp":          true,
	"sort":            true,
	"strconv":         true,
	"strings":         true,
	"time":            true,
	"unicode":         true,
	"unicode/utf8":    true,
}

// goEmbedExports builds the yaegi Exports (a filtered copy of
// stdlib.Symbols) restricted to goEmbedAllowlist. stdlib.Symbols keys are
// "<import path>/<package name>" (e.g. "encoding/json/json",
// "math/rand/v2/rand" for the v2 variant) — splitting on the *last* slash
// recovers the import path even for the nested v2 packages, so "math/rand"
// being allowed doesn't accidentally also let "math/rand/v2" through (that
// key's import path is "math/rand/v2", which fails the exact-match lookup
// below).
func goEmbedExports() interp.Exports {
	out := interp.Exports{}
	for key, syms := range stdlib.Symbols {
		i := strings.LastIndex(key, "/")
		if i < 0 {
			continue
		}
		if goEmbedAllowlist[key[:i]] {
			out[key] = syms
		}
	}
	return out
}

// errorType is reflect's handle on the `error` interface, used to check a
// run() function's second return value without constructing one.
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// ctxMapType is the exact parameter type run() must accept.
var ctxMapType = reflect.TypeOf(map[string]any{})

// anyType is the exact (non-error) return type run() must produce.
var anyType = reflect.TypeOf((*any)(nil)).Elem()

// execGoEmbed runs `run: go-embed` under yaegi, an interpreter for a large
// subset of real Go — the "you need actual control flow / types, but
// installing the Go toolchain on every conductor host is a bridge too far"
// engine (contrast `run: go`, which shells out to a real `go run` and so
// needs the toolchain present). It is sandboxed to goEmbedAllowlist and, by
// construction, cannot fork processes, touch the filesystem, or open a
// socket — the code is a Go snippet (imports + a `run` function) evaluated
// fresh, with no persistent state, once per call.
//
// The snippet's contract is that it defines exactly one of:
//
//	func run(ctx map[string]any) (any, error)
//	func run(ctx map[string]any) any
//
// checked by reflection after eval rather than assumed, so a snippet that
// gets the signature wrong (wrong param type, wrong return arity, a second
// return that isn't error, or no `run` at all) fails with a message naming
// the two accepted shapes instead of a confusing reflect panic mid-call.
func (e *Executor) execGoEmbed(spec Spec, data map[string]any) (map[string]any, error) {
	i := interp.New(interp.Options{})
	if err := i.Use(goEmbedExports()); err != nil {
		return nil, fmt.Errorf("code: go-embed: sandbox setup: %w", err)
	}

	if _, err := i.Eval(spec.Code); err != nil {
		return nil, fmt.Errorf("code: go-embed: %w", err)
	}

	runFn, err := i.Eval("run")
	if err != nil {
		return nil, fmt.Errorf("code: go-embed: %s: %w", goEmbedContractMsg, err)
	}

	if err := checkGoEmbedSignature(runFn); err != nil {
		return nil, err
	}

	results := runFn.Call([]reflect.Value{reflect.ValueOf(data)})
	if len(results) == 2 {
		if errVal, _ := results[1].Interface().(error); errVal != nil {
			return nil, fmt.Errorf("code: go-embed: run: %w", errVal)
		}
	}
	return wrapValue(results[0].Interface()), nil
}

const goEmbedContractMsg = "must define `func run(ctx map[string]any) (any, error)` or `func run(ctx map[string]any) any`"

// checkGoEmbedSignature validates runFn against the two accepted `run`
// shapes before it's ever called, so a mismatch surfaces as a clear
// contract error rather than a reflect panic (wrong argument count/type) or
// a silently-ignored second return value.
func checkGoEmbedSignature(runFn reflect.Value) error {
	if runFn.Kind() != reflect.Func {
		return fmt.Errorf("code: go-embed: run: %s, got %s", goEmbedContractMsg, runFn.Kind())
	}
	ft := runFn.Type()
	if ft.NumIn() != 1 || ft.In(0) != ctxMapType {
		return fmt.Errorf("code: go-embed: run: %s, got %s", goEmbedContractMsg, ft)
	}
	switch ft.NumOut() {
	case 1:
		if ft.Out(0) != anyType {
			return fmt.Errorf("code: go-embed: run: %s, got %s", goEmbedContractMsg, ft)
		}
	case 2:
		if ft.Out(0) != anyType || !ft.Out(1).Implements(errorType) {
			return fmt.Errorf("code: go-embed: run: %s, got %s", goEmbedContractMsg, ft)
		}
	default:
		return fmt.Errorf("code: go-embed: run: %s, got %s", goEmbedContractMsg, ft)
	}
	return nil
}
