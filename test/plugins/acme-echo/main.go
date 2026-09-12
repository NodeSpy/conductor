// Command acme-echo is a REFERENCE external connector plugin for conductor,
// built against the PUBLIC plugin SDK (github.com/NodeSpy/conductor/pkg/plugin)
// — it imports NO internal daemon package, exactly as a third-party plugin in
// its own module would. It provides a connector type "acme-echo" with one verb,
// echo, that returns its input, and makes no network calls.
//
// It exists to exercise and document the out-of-process plugin path end-to-end;
// the daemon's integration tests spawn this binary over the real transport, so
// its passing proves the SDK is byte-compatible with the daemon.
//
// Generic example only (acme/…) — not a real service. stdout is the RPC
// transport; all logging goes to stderr.
package main

import (
	"fmt"
	"os"

	plugin "github.com/NodeSpy/conductor/pkg/plugin"
)

func describe() plugin.Decl {
	return plugin.Decl{
		Type: "acme-echo",
		Desc: "reference echo connector (example plugin)",
		Connection: plugin.Schema{
			"token": {Type: "string", Desc: "an example credential the plugin receives per-call"},
		},
		Verbs: []plugin.Verb{{
			Name: "echo",
			Desc: "return the given message",
			Options: plugin.Schema{
				"message": {Type: "string", Required: true, Desc: "text to echo back"},
				"leak":    {Type: "boolean", Desc: "if true, log the received token to stderr (to exercise daemon redaction)"},
			},
			Outputs: plugin.Schema{
				"message":        {Type: "string", Required: true},
				"received_token": {Type: "boolean", Required: true},
			},
		}},
		Capabilities: plugin.Capabilities{}, // no egress, no fs, no spawn
	}
}

func invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	if req.Verb != "echo" {
		return plugin.InvokeResult{}, plugin.Errorf(plugin.CodeInvalidParams, "unknown verb "+req.Verb)
	}
	token, _ := req.Connection["token"].(string)
	// A buggy/malicious plugin might log its credential; the daemon redacts the
	// plugin's stderr, so this must never appear in the daemon log.
	if leak, _ := req.Options["leak"].(bool); leak {
		fmt.Fprintf(os.Stderr, "echo: DEBUG received token=%s\n", token)
	}
	// hang: never respond, to exercise the daemon's per-call timeout + supervision.
	if hang, _ := req.Options["hang"].(bool); hang {
		select {}
	}
	fmt.Fprintf(os.Stderr, "echo: invoked verb=%s instance=%s\n", req.Verb, req.Instance)
	// badoutput: return an undeclared/mistyped field, to exercise the daemon's
	// schema validation of untrusted output over the real wire.
	if bad, _ := req.Options["badoutput"].(bool); bad {
		return plugin.InvokeResult{Outputs: map[string]any{"surprise": "undeclared"}}, nil
	}
	msg, _ := req.Options["message"].(string)
	return plugin.InvokeResult{Outputs: map[string]any{
		"message":        msg,
		"received_token": token != "",
	}}, nil
}

func main() {
	// Support a --oversize self-test mode used by the negative e2e: emit a giant
	// describe result to prove the daemon's size cap rejects it. Raw stdout write,
	// bypassing the SDK, since the point is to violate the size bound.
	if len(os.Args) > 1 && os.Args[1] == "--oversize" {
		big := make([]byte, 32<<20)
		for i := range big {
			big[i] = 'a'
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":0,"result":{"protocol_version":1,"type":"acme-echo","desc":%q}}`+"\n", string(big))
		return
	}
	if err := plugin.Serve(plugin.ConnectorFunc(describe, invoke)); err != nil {
		fmt.Fprintf(os.Stderr, "acme-echo: serve: %v\n", err)
		os.Exit(1)
	}
}
