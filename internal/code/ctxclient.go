package code

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
)

// The REFERENCE CLIENT for the ctx socket — the thing that makes the data
// plane usable from a `use: cli` step without every operator hand-rolling a
// JSON-Lines-over-unix client in whatever language their command happens to
// be written in.
//
// It ships as conductor's own binary, because that is the one program a
// `cli` step is guaranteed to be able to run: the engine exports the daemon's
// executable path as CONDUCTOR_CTX_HELPER, and the step calls it.
//
//	# bash
//	v=$("$CONDUCTOR_CTX_HELPER" ctx kv cache get run "attempts")
//	"$CONDUCTOR_CTX_HELPER" ctx kv cache set run attempts 3
//	"$CONDUCTOR_CTX_HELPER" ctx sql analytics query 'SELECT n FROM t WHERE id = ?' '[7]'
//	"$CONDUCTOR_CTX_HELPER" ctx memory remember "the deploy needs a manual step" '[]' 'repo:acme/api'
//
// A shell pipeline of `nc`/`socat` was the alternative and is not one: it
// differs per platform, buffers where it should not, and gives a step no way
// to tell a policy refusal from a broken pipe.
//
// Nothing here is privileged. The client reads the same two environment
// variables the child was given, dials, and speaks the documented wire
// format; a step that would rather open the socket itself (a Python step
// with the json module, a Go step with net.Dial) is doing exactly what this
// does and is equally supported. See ctxsock.go for the protocol.

// CtxClientUsage is the one-screen help for `conductor ctx`.
const CtxClientUsage = `usage: conductor ctx <kind> [store] <op> [arg...]

  conductor ctx kv <store> <op> [arg...]       ns/key-positional, e.g. kv cache get run attempts
  conductor ctx sql <store> <op> <sql> [args]  args is a JSON list of bind values
  conductor ctx memory <op> [arg...]           remember|recall|forget|list

Each arg is parsed as JSON when it parses, and taken as a plain string when
it does not: 3 is a number, '{"a":1}' an object, hello the string "hello".

Reads CONDUCTOR_CTX_SOCK and CONDUCTOR_CTX_TOKEN from the environment — a
` + "`use: cli`" + ` step gets both. Prints the result value as JSON on stdout.

exit: 0 ok · 1 error · 2 usage · 3 refused by policy`

// Ctx client exit codes. 3 is distinct so a step can branch on "conductor
// will not let me do this" without parsing an error message.
const (
	ctxExitOK      = 0
	ctxExitError   = 1
	ctxExitUsage   = 2
	ctxExitRefused = 3
)

// CtxClientMain is the `conductor ctx …` entry point, and the reference
// implementation of the client half of the protocol. getenv is injected so
// the same code is testable without mutating the process environment.
func CtxClientMain(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	req, err := parseCtxArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "conductor ctx: %v\n\n%s\n", err, CtxClientUsage)
		return ctxExitUsage
	}
	sock, token := getenv("CONDUCTOR_CTX_SOCK"), getenv("CONDUCTOR_CTX_TOKEN")
	if sock == "" || token == "" {
		// The honest message for the two ways this happens: the step is not a
		// `cli` step at all, or it is a REMOTE one (the data plane does not
		// cross the ssh hop — see execCLIRemote).
		fmt.Fprintln(stderr, "conductor ctx: no ctx data plane in this environment "+
			"(CONDUCTOR_CTX_SOCK/CONDUCTOR_CTX_TOKEN unset) — available to a LOCAL `use: cli` step only")
		return ctxExitError
	}
	req.Token = token

	res, err := ctxRoundTrip(sock, req)
	if err != nil {
		fmt.Fprintf(stderr, "conductor ctx: %v\n", err)
		return ctxExitError
	}
	if !res.OK {
		fmt.Fprintf(stderr, "conductor ctx: %s\n", res.Error)
		if res.Refused {
			return ctxExitRefused
		}
		return ctxExitError
	}
	b, merr := json.Marshal(res.Value)
	if merr != nil {
		fmt.Fprintf(stderr, "conductor ctx: %v\n", merr)
		return ctxExitError
	}
	fmt.Fprintln(stdout, string(b))
	return ctxExitOK
}

// ctxRoundTrip dials the socket, sends one request line and reads one
// response line — the whole client half of the protocol, in one function, so
// a reader porting it to another language has one thing to port.
func ctxRoundTrip(sock string, req CtxRequest) (CtxResponse, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return CtxResponse{}, fmt.Errorf("dial %s: %w", sock, err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return CtxResponse{}, fmt.Errorf("send: %w", err)
	}
	var res CtxResponse
	if err := json.NewDecoder(c).Decode(&res); err != nil {
		return CtxResponse{}, fmt.Errorf("receive: %w", err)
	}
	return res, nil
}

// parseCtxArgs turns the argv into a CtxRequest. kv and sql name a store
// before the op (they address a DEFINED store — there is no default, same as
// every other face); memory does not (it has no store dimension).
func parseCtxArgs(args []string) (CtxRequest, error) {
	if len(args) == 0 {
		return CtxRequest{}, fmt.Errorf("want a kind (%s, %s or %s)", CtxKindKV, CtxKindSQL, CtxKindMemory)
	}
	req := CtxRequest{Kind: args[0]}
	rest := args[1:]
	switch req.Kind {
	case CtxKindKV, CtxKindSQL:
		if len(rest) < 2 {
			return CtxRequest{}, fmt.Errorf("%s wants a store and an op, e.g. `ctx %s <store> get …`", req.Kind, req.Kind)
		}
		req.Resource, req.Op, rest = rest[0], rest[1], rest[2:]
	case CtxKindMemory:
		if len(rest) < 1 {
			return CtxRequest{}, fmt.Errorf("memory wants an op (%s)", strings.Join(memOps, ", "))
		}
		req.Op, rest = rest[0], rest[1:]
	default:
		return CtxRequest{}, fmt.Errorf("no kind %q — want %s, %s or %s", req.Kind, CtxKindKV, CtxKindSQL, CtxKindMemory)
	}
	for _, a := range rest {
		req.Args = append(req.Args, ctxDecodeArg(a))
	}
	return req, nil
}

// ctxDecodeArg reads one argv word as a value. JSON when it is JSON, the raw
// string otherwise — so `set ns k hello` and `set ns k '{"a":1}'` both do the
// obvious thing and neither needs a flag. The fallback is what keeps the
// common case (a bare string key, a namespace) from having to be quoted
// twice through the shell.
func ctxDecodeArg(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}
